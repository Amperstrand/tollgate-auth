package main

import (
	"time"

	"tollgate-auth/internal/ledger"
)

// SettlementReport is the aggregate payload sent via NIP-17.
//
// Contains NO PII: no MAC addresses, no IPs, no token hashes, no individual sessions.
// Even though NIP-17 is encrypted, this struct is designed as if it could be
// decrypted someday — only aggregate revenue data leaves the ledger.
type SettlementReport struct {
	Type             string    `json:"type"` // always "settlement"
	OperatorID       string    `json:"operator_id"`
	PeriodStart      time.Time `json:"period_start"`
	PeriodEnd        time.Time `json:"period_end"`
	TotalSat         int       `json:"total_sat"`
	AcceptedSessions int       `json:"accepted_sessions"`
	RejectedSessions int       `json:"rejected_sessions"`
	AverageAmountSat float64   `json:"average_amount_sat"`
	GeneratedAt      time.Time `json:"generated_at"`
}

// BuildSettlementReport reads the ledger for the given operator over the time
// range and returns an aggregate SettlementReport containing no PII.
//
// The underlying RevenueSummary already strips per-session detail; this function
// only maps those aggregate fields onto the wire format. MAC addresses, IP
// addresses, token hashes, and individual session rows never enter the report.
func BuildSettlementReport(l *ledger.Ledger, operatorID string, start, end time.Time) (*SettlementReport, error) {
	rev, err := l.RevenueSummary(operatorID, start, end)
	if err != nil {
		return nil, err
	}

	return &SettlementReport{
		Type:             "settlement",
		OperatorID:       rev.OperatorID,
		PeriodStart:      rev.StartTime,
		PeriodEnd:        rev.EndTime,
		TotalSat:         rev.TotalSat,
		AcceptedSessions: rev.AcceptedSessions,
		RejectedSessions: rev.RejectedSessions,
		AverageAmountSat: rev.AverageAmount,
		GeneratedAt:      time.Now().UTC(),
	}, nil
}
