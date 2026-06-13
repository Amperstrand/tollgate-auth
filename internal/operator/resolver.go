package operator

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"time"
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
//   - TOLLGATE_CLASS_HMAC_KEY env var → hex-encoded key
//   - If not set, a random 32-byte key is generated (with a warning log)
func NewResolver(configPath string) (*Resolver, error) {
	r := &Resolver{}

	// Load HMAC key
	if keyHex := os.Getenv("TOLLGATE_CLASS_HMAC_KEY"); keyHex != "" {
		key, err := hex.DecodeString(keyHex)
		if err != nil {
			return nil, fmt.Errorf("operator resolver: invalid TOLLGATE_CLASS_HMAC_KEY (must be hex): %w", err)
		}
		if len(key) < 16 {
			return nil, fmt.Errorf("operator resolver: TOLLGATE_CLASS_HMAC_KEY too short (%d bytes, minimum 16)", len(key))
		}
		r.hmacKey = key
	} else {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("operator resolver: generating random HMAC key: %w", err)
		}
		r.hmacKey = key
		log.Printf("Warning: TOLLGATE_CLASS_HMAC_KEY not set, using random key (Class attributes will not survive restart)")
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
