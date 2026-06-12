package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"tollgate-auth/internal/radius"
	"tollgate-auth/internal/sessiond"
)

func handleAccounting() {
	if len(os.Args) < 5 {
		fmt.Fprintf(os.Stderr, "usage: %s --accounting <status-type> <acct-session-id> <calling-station-id> [user-name] [acct-session-time] [input-octets] [output-octets] [terminate-cause] [nas-ip-address]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  Called by FreeRADIUS exec module (tollgate-acct) for accounting packets.\n")
		os.Exit(1)
	}

	statusType := os.Args[2]
	acctSessionID := os.Args[3]
	mac := os.Args[4]

	username := argOr(os.Args, 5, "")
	sessionTimeStr := argOr(os.Args, 6, "")
	inputOctetsStr := argOr(os.Args, 7, "")
	outputOctetsStr := argOr(os.Args, 8, "")
	terminateCause := argOr(os.Args, 9, "")
	nasIP := argOr(os.Args, 10, "")

	log.Printf("Accounting: status=%s session=%s mac=%s user=%s time=%s in=%s out=%s cause=%s nas=%s",
		statusType, acctSessionID, mac, username, sessionTimeStr, inputOctetsStr, outputOctetsStr, terminateCause, nasIP)

	if authMode != "delegated" {
		log.Printf("Accounting: skipping (auth mode = %s, need delegated)", authMode)
		return
	}

	report := sessiond.UsageReport{
		Source:    "radius-accounting",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	switch statusType {
	case "Start":
		// Session just started — no usage data yet. Still report to register the session.
	case "Interim-Update", "3":
		report.SessionTime = parseUint64Ptr(sessionTimeStr)
		report.InputOctets = parseUint64Ptr(inputOctetsStr)
		report.OutputOctets = parseUint64Ptr(outputOctetsStr)
	case "Stop", "2":
		report.SessionTime = parseUint64Ptr(sessionTimeStr)
		report.InputOctets = parseUint64Ptr(inputOctetsStr)
		report.OutputOctets = parseUint64Ptr(outputOctetsStr)
	default:
		log.Printf("Accounting: unknown status type %q, forwarding anyway", statusType)
		report.SessionTime = parseUint64Ptr(sessionTimeStr)
		report.InputOctets = parseUint64Ptr(inputOctetsStr)
		report.OutputOctets = parseUint64Ptr(outputOctetsStr)
	}

	client := sessiond.NewClient(sessiondURL)
	apiKey := getEnv("TOLLGATE_API_KEY", "")
	resp, err := client.ReportUsage(mac, report, apiKey)
	if err != nil {
		log.Printf("Accounting: ReportUsage failed for mac=%s: %v", mac, err)
		// Don't fail accounting on session daemon error — FreeRADIUS accounting must succeed
		return
	}

	log.Printf("Accounting: mac=%s status=%s level=%s remaining=%d is_final=%v",
		mac, statusType, resp.AccessLevel, resp.RemainingQuota, resp.IsFinal)

	if resp.AccessLevel == "suspended" && nasIP != "" {
		coaSecret := getEnv("TOLLGATE_COA_SECRET", "tollgate")
		coaPort := getEnv("TOLLGATE_COA_PORT", "3799")
		log.Printf("CoA: session suspended for mac=%s, sending disconnect to %s:%s", mac, nasIP, coaPort)
		if err := sendDisconnect(nasIP, coaPort, acctSessionID, username, coaSecret); err != nil {
			log.Printf("CoA: disconnect failed for mac=%s nas=%s: %v", mac, nasIP, err)
		} else {
			log.Printf("CoA: disconnect sent for mac=%s nas=%s session=%s", mac, nasIP, acctSessionID)
		}
	}
}

// sendDisconnect sends a RADIUS Disconnect-Request to terminate a session on the NAS.
// Delegates to the native Go RADIUS client in internal/radius.
func sendDisconnect(nasIP, port, acctSessionID, username, secret string) error {
	nasAddr := fmt.Sprintf("%s:%s", nasIP, port)
	return radius.SendDisconnect(context.Background(), nasAddr, secret, acctSessionID, username)
}

func parseUint64Ptr(s string) *uint64 {
	if s == "" {
		return nil
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return nil
	}
	return &v
}

func argOr(args []string, index int, fallback string) string {
	if index < len(args) {
		return args[index]
	}
	return fallback
}
