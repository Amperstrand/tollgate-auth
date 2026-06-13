package operator

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/crypto/hkdf"
)

// Resolver combines env, config file, and registry sources to resolve
// the operator identity for a given RADIUS request.
type Resolver struct {
	registry *OperatorRegistry
	envOp    *OperatorContext
	hmacKey  []byte
}

// NewResolver creates a Resolver by loading configuration from environment
// variables and optional config file paths.
//
// Resolution order for operator identity:
//  1. TOLLGATE_OPERATOR_REGISTRY env var → path to multi-operator JSON config
//  2. TOLLGATE_OPERATOR_NPUB env var → single operator npub
//  3. If neither is set, the resolver returns "anonymous" operators
//
// HMAC key for Class signing:
//   - Derived from TOLLGATE_OPERATOR_NSEC via HKDF-SHA256 with domain separator "tollgate/radius-class-hmac-v1"
//   - Hard error if TOLLGATE_OPERATOR_NSEC is not set (no fallback)
func NewResolver(configPath string) (*Resolver, error) {
	r := &Resolver{}

	// Load operator nsec for HMAC key derivation
	nsec := os.Getenv("TOLLGATE_OPERATOR_NSEC")
	if nsec == "" {
		return nil, fmt.Errorf("operator resolver: TOLLGATE_OPERATOR_NSEC not set — HMAC key derivation requires operator nsec. Set TOLLGATE_OPERATOR_NSEC to your Nostr private key (nsec1...)")
	}

	nsecBytes, err := decodeNsec(nsec)
	if err != nil {
		return nil, fmt.Errorf("operator resolver: invalid TOLLGATE_OPERATOR_NSEC: %w", err)
	}

	// Derive HMAC key from nsec via HKDF with domain separation
	hkdfReader := hkdf.New(sha256.New, nsecBytes, []byte("tollgate"), []byte("radius-class-hmac-v1"))
	r.hmacKey = make([]byte, 32)
	if _, err := io.ReadFull(hkdfReader, r.hmacKey); err != nil {
		return nil, fmt.Errorf("operator resolver: HKDF key derivation failed: %w", err)
	}

	// Load operator registry from config path (env override or argument)
	registryPath := os.Getenv("TOLLGATE_OPERATOR_REGISTRY")
	if registryPath == "" && configPath != "" {
		registryPath = configPath
	}
	if registryPath != "" {
		reg, err := LoadRegistry(registryPath)
		if err != nil {
			return nil, fmt.Errorf("operator resolver: loading registry from %q: %w", registryPath, err)
		}
		r.registry = reg
	} else {
		r.registry = &OperatorRegistry{}
	}

	// Load single operator from env as fallback
	envOp, err := ParseOperatorFromEnv()
	if err != nil {
		return nil, fmt.Errorf("operator resolver: %w", err)
	}
	r.envOp = envOp

	return r, nil
}

// anonymousContext returns a default "anonymous" operator context.
func anonymousContext() *OperatorContext {
	return &OperatorContext{
		Account: OperatorAccount{
			ID:        "anonymous",
			CreatedAt: time.Now(),
		},
		Source:   "default",
		Resolved: false,
	}
}

// Resolve determines the operator identity for a RADIUS request.
//
// Resolution order:
//  1. Registry lookup by clientIP then nasID (exact match)
//  2. Registry default operator
//  3. Environment npub (TOLLGATE_OPERATOR_NPUB)
//  4. Anonymous fallback (ID "anonymous")
func (r *Resolver) Resolve(clientIP, nasID string) *OperatorContext {
	// Try registry resolution
	if r.registry != nil {
		if ctx := r.registry.Resolve(clientIP, nasID); ctx != nil {
			return ctx
		}
	}

	// Fallback to env operator
	if r.envOp != nil {
		return r.envOp
	}

	// Anonymous fallback
	return anonymousContext()
}

// HMACKey returns the HMAC key used for Class attribute signing.
func (r *Resolver) HMACKey() []byte {
	return r.hmacKey
}
