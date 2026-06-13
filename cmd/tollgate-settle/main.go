package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"tollgate-auth/internal/config"
	"tollgate-auth/internal/ledger"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"fiatjaf.com/nostr/nip17"
	"fiatjaf.com/nostr/nip19"
)

// Default relay list used when TOLLGATE_RELAYS is unset.
var defaultRelays = []string{"wss://relay.damus.io", "wss://nos.lol"}

// parseRelays splits a comma-separated relay list, trimming whitespace and
// dropping empties. Returns the default list when the input is empty.
func parseRelays(raw string) []string {
	if raw == "" {
		return defaultRelays
	}
	parts := strings.Split(raw, ",")
	relays := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			relays = append(relays, p)
		}
	}
	if len(relays) == 0 {
		return defaultRelays
	}
	return relays
}

// decodeNsec decodes an nsec bech32 string into a nostr.SecretKey.
func decodeNsec(nsec string) (nostr.SecretKey, error) {
	prefix, value, err := nip19.Decode(nsec)
	if err != nil {
		return nostr.SecretKey{}, fmt.Errorf("decode nsec: %w", err)
	}
	if prefix != "nsec" {
		return nostr.SecretKey{}, fmt.Errorf("expected nsec prefix, got %q", prefix)
	}
	sk, ok := value.(nostr.SecretKey)
	if !ok {
		return nostr.SecretKey{}, fmt.Errorf("decoded nsec value is not a SecretKey (%T)", value)
	}
	return sk, nil
}

// decodeNpub decodes an npub bech32 string into a nostr.PubKey.
func decodeNpub(npub string) (nostr.PubKey, error) {
	prefix, value, err := nip19.Decode(npub)
	if err != nil {
		return nostr.PubKey{}, fmt.Errorf("decode npub: %w", err)
	}
	if prefix != "npub" {
		return nostr.PubKey{}, fmt.Errorf("expected npub prefix, got %q", prefix)
	}
	pk, ok := value.(nostr.PubKey)
	if !ok {
		return nostr.PubKey{}, fmt.Errorf("decoded npub value is not a PubKey (%T)", value)
	}
	return pk, nil
}

// sendSettlementDM publishes the encrypted settlement JSON to the recipient via
// NIP-17 gift-wrapped direct message. Returns nil on success.
//
// The payload is aggregate-only by construction (SettlementReport), so even if
// the gift-wrap is someday unwrapped by a third party it leaks no PII.
func sendSettlementDM(ctx context.Context, relays []string, kr nostr.Keyer, recipient nostr.PubKey, payload []byte) error {
	pool := nostr.NewPool()
	defer pool.Relays.Range(func(url string, _ *nostr.Relay) bool { return true })

	content := string(payload)
	return nip17.PublishMessage(
		ctx,
		content,
		nil, // no extra tags
		pool,
		relays, // our relays
		relays, // their relays (assume same set for self-settlement)
		kr,
		recipient,
		nil, // no event modification
	)
}

func main() {
	ledgerPath := flag.String("ledger", "/opt/tollgate-auth/ledger.jsonl", "path to the JSONL ledger")
	operatorID := flag.String("operator", config.GetEnv("TOLLGATE_OPERATOR_ID", "default"), "operator ID to summarize")
	sinceStr := flag.String("since", time.Now().UTC().Add(-7*24*time.Hour).Format(time.RFC3339), "period start (RFC3339)")
	untilStr := flag.String("until", time.Now().UTC().Format(time.RFC3339), "period end (RFC3339)")
	relaysFlag := flag.String("relays", config.GetEnv("TOLLGATE_RELAYS", ""), "comma-separated Nostr relay URLs")
	dryRun := flag.Bool("dry-run", false, "print the settlement report to stdout without sending a Nostr DM")
	flag.Parse()

	start, err := time.Parse(time.RFC3339, *sinceStr)
	if err != nil {
		log.Fatalf("invalid --since %q: %v", *sinceStr, err)
	}
	end, err := time.Parse(time.RFC3339, *untilStr)
	if err != nil {
		log.Fatalf("invalid --until %q: %v", *untilStr, err)
	}

	relays := parseRelays(*relaysFlag)

	log.Printf("tollgate-settle: operator=%s ledger=%s period=%s..%s relays=%d dry-run=%v",
		*operatorID, *ledgerPath, start.Format(time.RFC3339), end.Format(time.RFC3339), len(relays), *dryRun)

	l, err := ledger.OpenLedger(*ledgerPath)
	if err != nil {
		log.Fatalf("open ledger: %v", err)
	}
	defer l.Close()

	report, err := BuildSettlementReport(l, *operatorID, start, end)
	if err != nil {
		log.Fatalf("build settlement report: %v", err)
	}

	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		log.Fatalf("marshal report: %v", err)
	}

	if *dryRun {
		fmt.Println(string(payload))
		return
	}

	// Sending mode: require Nostr credentials.
	nsec := os.Getenv("TOLLGATE_OPERATOR_NSEC")
	npub := os.Getenv("TOLLGATE_OPERATOR_NPUB")
	if nsec == "" {
		log.Fatal("TOLLGATE_OPERATOR_NSEC is required (sender nsec)")
	}
	if npub == "" {
		log.Fatal("TOLLGATE_OPERATOR_NPUB is required (recipient npub)")
	}

	sk, err := decodeNsec(nsec)
	if err != nil {
		log.Fatalf("decode sender nsec: %v", err)
	}
	recipient, err := decodeNpub(npub)
	if err != nil {
		log.Fatalf("decode recipient npub: %v", err)
	}

	kr := keyer.NewPlainKeySigner(sk)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log.Printf("sending settlement DM: total_sat=%d accepted=%d rejected=%d",
		report.TotalSat, report.AcceptedSessions, report.RejectedSessions)

	if err := sendSettlementDM(ctx, relays, kr, recipient, payload); err != nil {
		log.Fatalf("send settlement DM: %v", err)
	}

	log.Printf("settlement DM sent to operator %s", *operatorID)
}
