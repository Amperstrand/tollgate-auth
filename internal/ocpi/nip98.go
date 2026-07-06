package ocpi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"fiatjaf.com/nostr"
)

const nip98Kind = nostr.KindHTTPAuth

const nip98MaxAge = 60 * time.Second
const nip98MaxFuture = 30 * time.Second

type nip98Result struct {
	PubKeyHex string
	Valid     bool
}

func verifyNIP98(r *http.Request) nip98Result {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return nip98Result{}
	}

	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Nostr") {
		return nip98Result{}
	}

	raw, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nip98Result{}
	}

	var evt nostr.Event
	if err := json.Unmarshal(raw, &evt); err != nil {
		return nip98Result{}
	}

	if evt.Kind != nip98Kind {
		return nip98Result{}
	}

	age := time.Since(evt.CreatedAt.Time())
	if age > nip98MaxAge || age < -nip98MaxFuture {
		return nip98Result{}
	}

	if !evt.VerifySignature() {
		return nip98Result{}
	}

	uTag := evt.Tags.Find("u")
	if uTag == nil || len(uTag) < 2 {
		return nip98Result{}
	}

	expectedURL := "https://" + r.Host + r.URL.Path
	if uTag[1] != expectedURL && uTag[1] != r.URL.Path {
		return nip98Result{}
	}

	return nip98Result{PubKeyHex: evt.PubKey.Hex(), Valid: true}
}
