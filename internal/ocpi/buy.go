package ocpi

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"tollgate-auth/internal/cashu"
)

type BuyRequest struct {
	AmountEur    float64 `json:"amount_eur"`
	Phone        string  `json:"phone,omitempty"`
	ProviderNpub string  `json:"provider_npub,omitempty"`
}

type BuyResponse struct {
	Token        string `json:"token"`
	AmountEur    string `json:"amount_eur"`
	MintURL      string `json:"mint_url"`
	QuoteID      string `json:"quote_id"`
	PaymentState string `json:"payment_state"`
	ProviderNpub string `json:"provider_npub,omitempty"`
}

func (s *Server) HandleBuy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, Err(StatusUnknownMethod, "POST required"))
		return
	}

	var body BuyRequest
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil {
		writeJSON(w, Err(StatusClientError, "cannot read body"))
		return
	}
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		writeJSON(w, Err(StatusClientError, "invalid JSON"))
		return
	}
	if body.AmountEur <= 0 || body.AmountEur > 1000 {
		writeJSON(w, Err(StatusClientError, "amount_eur must be between 0 and 1000"))
		return
	}

	mintURL := s.cfg.EurMintURL
	if mintURL == "" {
		mintURL = "http://127.0.0.1:3340"
	}
	amountCents := int(body.AmountEur * 100)

	slog.Info("buy request", "amount_eur", body.AmountEur, "amount_cents", amountCents, "phone", body.Phone, "provider", body.ProviderNpub)

	token, err := mintEurToken(mintURL, amountCents)
	if err != nil {
		slog.Error("buy: mint failed", "error", err)
		writeJSON(w, Err(StatusClientError, "mint failed: "+err.Error()))
		return
	}

	if body.ProviderNpub != "" {
		tokenHash := cashu.TokenHash(token)
		s.providers.PutTokenProvider(&TokenProvider{
			TokenHash:    tokenHash,
			ProviderNpub: body.ProviderNpub,
			Amount:       amountCents,
			Unit:         "eur",
		})
		slog.Info("buy: token bound to provider", "provider", body.ProviderNpub, "token_hash_prefix", tokenHash[:16])
	}

	slog.Info("buy success", "amount_eur", body.AmountEur, "token_prefix", safePrefix(token, 30))
	writeJSON(w, OK(BuyResponse{
		Token:        token,
		AmountEur:    fmt.Sprintf("%.2f", body.AmountEur),
		MintURL:      mintURL,
		PaymentState: "PAID",
		ProviderNpub: body.ProviderNpub,
	}))
}

// mintEurToken mints EUR-denominated Cashu tokens via cdk-cli using a throwaway
// wallet directory. A fresh temp wallet per call avoids state pollution: each
// buy gets a clean mint→send cycle, so the wallet balance is always exactly the
// requested amount and the send never fails with "insufficient funds".
func mintEurToken(mintURL string, amountCents int) (string, error) {
	walletDir, err := os.MkdirTemp("", "cashu-buy-*")
	if err != nil {
		return "", fmt.Errorf("create temp wallet: %w", err)
	}
	defer os.RemoveAll(walletDir)

	mintCmd := exec.Command("cdk-cli",
		"--work-dir", walletDir,
		"--unit", "eur",
		"mint", mintURL, strconv.Itoa(amountCents),
		"tollgate buy",
		"--wait-duration", "10",
	)
	mintCmd.Env = []string{"HOME=/tmp", "PATH=" + os.Getenv("PATH")}
	mintOut, err := mintCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("cdk-cli mint failed: %w: %s", err, safePrefix(string(mintOut), 300))
	}
	if !strings.Contains(string(mintOut), "Received") && !strings.Contains(string(mintOut), "Minted") {
		return "", fmt.Errorf("mint did not complete: %s", safePrefix(string(mintOut), 300))
	}

	sendCmd := exec.Command("cdk-cli",
		"--work-dir", walletDir,
		"--unit", "eur",
		"send", "--amount", strconv.Itoa(amountCents),
		"--mint-url", mintURL,
	)
	sendCmd.Env = []string{"HOME=/tmp", "PATH=" + os.Getenv("PATH")}
	sendOut, err := sendCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("cdk-cli send failed: %w: %s", err, safePrefix(string(sendOut), 300))
	}
	for _, line := range strings.Split(string(sendOut), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "cashuA") || strings.HasPrefix(line, "cashuB") {
			return line, nil
		}
	}
	return "", fmt.Errorf("send produced no token: %s", safePrefix(string(sendOut), 300))
}
