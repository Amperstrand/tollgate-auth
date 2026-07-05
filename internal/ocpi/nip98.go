package ocpi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"fiatjaf.com/nostr"
)

const nip98Kind = nostr.KindHTTPAuth

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

	if !evt.VerifySignature() {
		return nip98Result{}
	}

	uTag := evt.Tags.Find("u")
	if uTag == nil {
		return nip98Result{}
	}

	expectedPath := r.URL.Path
	if r.Host != "" {
		if strings.Contains(uTag[1], r.Host) {
			return nip98Result{PubKeyHex: evt.PubKey.Hex(), Valid: true}
		}
	}
	if uTag[1] == expectedPath || strings.HasSuffix(uTag[1], expectedPath) {
		return nip98Result{PubKeyHex: evt.PubKey.Hex(), Valid: true}
	}

	return nip98Result{}
}
