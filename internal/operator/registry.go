package operator

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// OperatorMatch defines how to identify an operator's RADIUS traffic.
type OperatorMatch struct {
	ClientIP string // RADIUS client IP (AP/NAS address)
	NASID    string // NAS-Identifier attribute
	Default  bool   // fallback operator when no match
}

// RegisteredOperator pairs an operator account with match criteria.
type RegisteredOperator struct {
	Account OperatorAccount
	Match   OperatorMatch
}

// OperatorRegistry holds loaded operators for runtime resolution.
type OperatorRegistry struct {
	operators []RegisteredOperator
	defaultOp *RegisteredOperator
}

// registryConfigFile is the JSON structure for the operators config file.
type registryConfigFile struct {
	Operators []registryOperatorEntry `json:"operators"`
}

type registryOperatorEntry struct {
	ID            string `json:"id"`
	PayoutNpub    string `json:"payout_npub"`
	PayoutLNURL   string `json:"payout_lnurl"`
	PayoutAddress string `json:"payout_address"`
	Match         struct {
		ClientIP string `json:"client_ip"`
		NASID    string `json:"nas_id"`
		Default  bool   `json:"default"`
	} `json:"match"`
}

// LoadRegistry reads a JSON config file and returns an OperatorRegistry.
// Returns an empty registry (no error) if the file doesn't exist.
// Returns an error if the file exists but has invalid JSON.
// Validates each operator — skips invalid ones with a log message.
func LoadRegistry(path string) (*OperatorRegistry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &OperatorRegistry{}, nil
		}
		return nil, fmt.Errorf("reading operators config: %w", err)
	}

	var cfg registryConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing operators config: %w", err)
	}

	reg := &OperatorRegistry{}
	for _, entry := range cfg.Operators {
		acc := OperatorAccount{
			ID:            entry.ID,
			PayoutNpub:    entry.PayoutNpub,
			PayoutLNURL:   entry.PayoutLNURL,
			PayoutAddress: entry.PayoutAddress,
			CreatedAt:     time.Now(),
		}

		if err := ValidateOperatorAccount(acc); err != nil {
			log.Printf("operator registry: skipping operator %q: %v", entry.ID, err)
			continue
		}

		op := RegisteredOperator{
			Account: acc,
			Match: OperatorMatch{
				ClientIP: entry.Match.ClientIP,
				NASID:    entry.Match.NASID,
				Default:  entry.Match.Default,
			},
		}

		reg.operators = append(reg.operators, op)
		if op.Match.Default {
			reg.defaultOp = &reg.operators[len(reg.operators)-1]
		}
	}

	return reg, nil
}

// Resolve finds the matching operator for a given RADIUS client IP and NAS-ID.
// Priority: exact client_ip match > exact nas_id match > default operator > nil
// If multiple operators match by client_ip, first match wins.
func (r *OperatorRegistry) Resolve(clientIP, nasID string) *OperatorContext {
	// First pass: exact client_ip match
	for i := range r.operators {
		if r.operators[i].Match.ClientIP != "" && r.operators[i].Match.ClientIP == clientIP {
			return &OperatorContext{
				Account:  r.operators[i].Account,
				Source:   "config-file",
				Resolved: true,
			}
		}
	}

	// Second pass: exact nas_id match (case-insensitive)
	for i := range r.operators {
		if r.operators[i].Match.NASID != "" && strings.EqualFold(r.operators[i].Match.NASID, nasID) {
			return &OperatorContext{
				Account:  r.operators[i].Account,
				Source:   "config-file",
				Resolved: true,
			}
		}
	}

	// Third: default operator
	if r.defaultOp != nil {
		return &OperatorContext{
			Account:  r.defaultOp.Account,
			Source:   "config-file",
			Resolved: true,
		}
	}

	return nil
}

// Operators returns the list of registered operators (for introspection/debugging).
func (r *OperatorRegistry) Operators() []RegisteredOperator {
	return r.operators
}
