package main

import (
	"fmt"
	"log"
	"os"

	"tollgate-auth/internal/auth"
	"tollgate-auth/internal/cashu"
	"tollgate-auth/internal/fakeverity"
)

var (
	walletDir     = getenv("WALLET_DIR", "/var/lib/cashu-wallet")
	rateSecPerSat = 60
	cashuVerifier fakeverity.Verifier
)

func initCashu() {
	if _, err := os.Stat(walletDir); err == nil {
		cashuVerifier = fakeverity.NewProductionVerifier(walletDir)
		log.Printf("[tollgate-fcd] Cashu verifier initialized (wallet=%s, rate=%ds/sat)", walletDir, rateSecPerSat)
	} else {
		log.Printf("[tollgate-fcd] Wallet dir %s not found, Cashu verification disabled", walletDir)
	}
}

type cashuResult struct {
	Accept  bool
	Seconds int
	Amount  int
	Mint    string
	Error   string
}

func verifyCashuToken(token string) cashuResult {
	if cashuVerifier == nil {
		return cashuResult{Accept: false, Error: "Cashu verification not configured (no wallet)"}
	}

	tokenData, err := cashuVerifier.Decode(token)
	if err != nil {
		return cashuResult{Accept: false, Error: fmt.Sprintf("invalid token: %v", err)}
	}

	if tokenData.Amount <= 0 {
		return cashuResult{Accept: false, Error: "token has zero value"}
	}

	if !auth.IsTestMint(tokenData.Mint) {
		return cashuResult{Accept: false, Error: fmt.Sprintf("mint not allowed: %s", tokenData.Mint)}
	}

	state, msg := cashuVerifier.CheckState(tokenData)
	switch state {
	case cashu.StateUnspent:
		if err := cashuVerifier.Redeem(token); err != nil {
			return cashuResult{Accept: false, Error: fmt.Sprintf("redemption failed: %v", err)}
		}
		seconds := tokenData.Amount * rateSecPerSat
		thash := cashu.TokenHash(token)
		log.Printf("[tollgate-fcd] Cashu accepted: amount=%d %s, duration=%ds, mint=%s, hash=%s",
			tokenData.Amount, tokenData.Unit, seconds, tokenData.Mint, thash[:16])
		return cashuResult{
			Accept:  true,
			Seconds: seconds,
			Amount:  tokenData.Amount,
			Mint:    tokenData.Mint,
		}

	case cashu.StateSpent:
		return cashuResult{Accept: false, Error: "token already spent"}

	case cashu.StatePending:
		return cashuResult{Accept: false, Error: "token pending, try again"}

	default:
		return cashuResult{Accept: false, Error: fmt.Sprintf("cannot verify: %s", msg)}
	}
}
