// cmd/tollgate-auth-ocpi is the OCPI 2.2.1 eMSP receiver binary.
//
// It exposes the receiver endpoints a CPO (or OCPI test platform like OCPPLab)
// calls during roaming. Real-time token authorize delegates to the shared
// internal/auth.ProcessAuth pipeline so the same Cashu payment gate that powers
// SSH (tollgate-auth-ssh) and WiFi (tollgate-auth-radius) now gates EV charging.
//
// Architecture:
//
//	OCPPLab (CPO) → POST /ocpi/emsp/2.2.1/tokens/{uid}/authorize → tollgate-auth-ocpi
//	                                                                         ↓
//	                                                              internal/auth.ProcessAuth
//	                                                                         ↓
//	                                                              internal/sessiond → tollgate-rs
//
// All Cashu decoding, mint verification, replay protection, and ledger entries
// are reused unchanged from the RADIUS path.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"tollgate-auth/internal/auth"
	"tollgate-auth/internal/config"
	"tollgate-auth/internal/fakeverity"
	"tollgate-auth/internal/ledger"
	"tollgate-auth/internal/ocpi"
	"tollgate-auth/internal/operator"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	// --- Configuration ---
	listenAddr := config.GetEnv("TOLLGATE_OCPI_ADDR", ":8093")
	publicBase := config.GetEnv("TOLLGATE_OCPI_PUBLIC_URL", "http://localhost:8092")
	ourCountry := config.GetEnv("TOLLGATE_OCPI_COUNTRY", "NO")
	ourParty := config.GetEnv("TOLLGATE_OCPI_PARTY", "TGA")
	bootstrapToken := config.GetEnv("TOLLGATE_OCPI_TOKEN_A", "") // empty = accept any (PoC)
	dashBase := config.GetEnv("TOLLGATE_OCPI_DASH_URL", publicBase)

	baseDir := config.GetEnv("TOLLGATE_BASE_DIR", "/opt/tollgate-auth")
	walletDir := config.GetEnv("TOLLGATE_WALLET_DIR", "/var/lib/cashu-wallet")
	authMode := config.GetEnv("TOLLGATE_AUTH_MODE", "delegated")
	sessiondURL := config.GetEnv("TOLLGATE_SESSIOND_URL", "http://127.0.0.1:2121")

	// --- Initialize shared dependencies (same pattern as tollgate-daemon) ---
	sessionsDir := baseDir + "/ocpi-sessions"
	if err := os.MkdirAll(sessionsDir, 0700); err != nil {
		slog.Error("mkdir sessions", "error", err)
		os.Exit(1)
	}
	sessions := &auth.SessionStore{Dir: sessionsDir}

	if err := os.MkdirAll(baseDir, 0755); err != nil {
		slog.Error("mkdir base", "error", err)
		os.Exit(1)
	}

	opResolver, err := operator.NewResolver("")
	if err != nil {
		slog.Error("operator resolver", "error", err)
		os.Exit(1)
	}
	opCtx := opResolver.Resolve("", "")

	prodVerifier := fakeverity.NewProductionVerifier(walletDir)
	var verifier fakeverity.Verifier = prodVerifier
	if config.GetEnv("TOLLGATE_OCPI_REDEEM", "false") != "true" {
		slog.Warn("verify-only mode enabled (TOLLGATE_OCPI_REDEEM != true). " +
			"Tokens will be verified against the mint but not redeemed. " +
			"Set TOLLGATE_OCPI_REDEEM=true and install cdk-cli to enable value transfer.")
		verifier = newVerifyOnlyVerifier(prodVerifier)
	}
	var bootstrapper fakeverity.Bootstrapper
	if authMode == "delegated" {
		bootstrapper = fakeverity.NewProductionBootstrapper(sessiondURL)
		slog.Info("using delegated mode", "sessiond_url", sessiondURL)
	} else {
		slog.Info("using local mode (direct mint verification)")
	}

	var lg *ledger.Ledger
	if path := config.GetEnv("TOLLGATE_LEDGER_PATH", baseDir+"/ocpi-ledger.jsonl"); path != "" {
		lg, err = ledger.OpenLedger(path)
		if err != nil {
			slog.Warn("ledger open failed, continuing without", "path", path, "error", err)
			lg = nil
		}
	}

	deps := &auth.Dependencies{
		Sessions:     sessions,
		Verifier:     verifier,
		Bootstrapper: bootstrapper,
		OperatorID:   opCtx.Account.ID,
		HMACKey:      opResolver.HMACKey(),
		AuthMode:     authMode,
		SessiondURL:  sessiondURL,
		Ledger:       lg,
	}

	// --- Build & start OCPI server ---
	cfg := ocpi.Config{
		ListenAddr:     listenAddr,
		PublicBaseURL:  publicBase,
		OurCountry:     ourCountry,
		OurParty:       ourParty,
		BootstrapToken: bootstrapToken,
		DashboardBase:  dashBase,
	}
	srv := ocpi.NewServerWithLedger(cfg, deps, lg)

	// --- Signal handling ---
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	slog.Info("startup",
		"listen", listenAddr,
		"public_url", publicBase,
		"party", fmt.Sprintf("%s/%s", ourCountry, ourParty),
		"mode", authMode,
		"operator", opCtx.Account.ID,
	)

	if err := srv.Start(ctx); err != nil {
		slog.Error("server stopped with error", "error", err)
		os.Exit(1)
	}
	slog.Info("shutdown complete")
}
