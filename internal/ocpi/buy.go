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
)

type BuyRequest struct {
	AmountEur float64 `json:"amount_eur"`
	Phone     string  `json:"phone,omitempty"`
}

type BuyResponse struct {
	Token        string `json:"token"`
	AmountEur    string `json:"amount_eur"`
	MintURL      string `json:"mint_url"`
	QuoteID      string `json:"quote_id"`
	PaymentState string `json:"payment_state"`
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
	walletDir := "/var/lib/cashu-wallet"
	amountSat := int(body.AmountEur * 100)

	slog.Info("buy request", "amount_eur", body.AmountEur, "amount_sat", amountSat, "phone", body.Phone)

	token, err := mintViaCdkCli(mintURL, walletDir, amountSat)
	if err != nil {
		slog.Error("buy: cdk-cli mint failed", "error", err)
		writeJSON(w, Err(StatusClientError, "mint failed: "+err.Error()))
		return
	}

	slog.Info("buy success", "amount_eur", body.AmountEur, "token_prefix", safePrefix(token, 30))
	writeJSON(w, OK(BuyResponse{
		Token:        token,
		AmountEur:    fmt.Sprintf("%.2f", body.AmountEur),
		MintURL:      mintURL,
		PaymentState: "PAID",
	}))
}

func mintViaCdkCli(mintURL, walletDir string, amountSat int) (string, error) {
	cmd := exec.Command("cdk-cli",
		"--work-dir", walletDir,
		"--unit", "sat",
		"mint", mintURL, strconv.Itoa(amountSat),
		"tollgate buy endpoint",
		"--wait-duration", "10",
	)
	cmd.Env = []string{"HOME=/tmp", "PATH=" + os.Getenv("PATH")}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("cdk-cli failed: %w: %s", err, string(output))
	}

	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "cashuA") || strings.HasPrefix(line, "cashuB") {
			line = strings.TrimRight(line, "=\n\r ")
			return line, nil
		}
	}

	if strings.Contains(string(output), "Received") {
		token, err := sendFromWallet(walletDir, amountSat)
		if err != nil {
			return "", fmt.Errorf("minted but send failed: %w", err)
		}
		return token, nil
	}

	return "", fmt.Errorf("cdk-cli produced no token: %s", safePrefix(string(output), 200))
}

func sendFromWallet(walletDir string, amountSat int) (string, error) {
	cmd := exec.Command("cdk-cli",
		"--work-dir", walletDir,
		"--unit", "sat",
		"send", "--amount", strconv.Itoa(amountSat),
		"--mint-url", "http://127.0.0.1:3340",
	)
	cmd.Env = []string{"HOME=/tmp", "PATH=" + os.Getenv("PATH")}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("cdk-cli send failed: %w: %s", err, string(output))
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "cashuA") || strings.HasPrefix(line, "cashuB") {
			line = strings.TrimRight(line, "=\n\r ")
			return line, nil
		}
	}
	return "", fmt.Errorf("cdk-cli send produced no token: %s", safePrefix(string(output), 200))
}
