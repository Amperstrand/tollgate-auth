// verify_only.go defines a Verifier wrapper that delegates decode/verify/checkstate
// to a real ProductionVerifier but treats Redeem as a logged no-op.
//
// Why this exists: PoC mode. Real verify (NUT-07 /v1/checkstate against the
// mint) confirms the user's proofs are valid and unspent — that's enough to
// demonstrate "Cashu gates an EV charging session." Actual redemption (NUT-03
// swap into our wallet via cdk-cli) is what transfers value; for a PoC demo
// we don't need to claim the tokens, just verify them.
//
// DO NOT use in production: without redeem, the same token can be reused
// infinitely (mitigated only by our local replay guard). Real deployments
// install cdk-cli and use the real ProductionVerifier without this wrapper.
package main

import (
	"log/slog"

	"tollgate-auth/internal/cashu"
	"tollgate-auth/internal/fakeverity"
)

type verifyOnlyVerifier struct {
	inner fakeverity.Verifier
}

// newVerifyOnlyVerifier wraps a ProductionVerifier with no-op Redeem.
func newVerifyOnlyVerifier(inner fakeverity.Verifier) fakeverity.Verifier {
	return &verifyOnlyVerifier{inner: inner}
}

func (v *verifyOnlyVerifier) Decode(tokenStr string) (*cashu.TokenData, error) {
	return v.inner.Decode(tokenStr)
}

func (v *verifyOnlyVerifier) Verify(tokenData *cashu.TokenData) (bool, string) {
	return v.inner.Verify(tokenData)
}

func (v *verifyOnlyVerifier) CheckState(tokenData *cashu.TokenData) (cashu.ProofState, string) {
	return v.inner.CheckState(tokenData)
}

func (v *verifyOnlyVerifier) Redeem(tokenStr string) error {
	slog.Warn("verify-only mode: skipping token redemption (PoC, set TOLLGATE_OCPI_REDEEM=true to enable cdk-cli redemption)")
	return nil
}
