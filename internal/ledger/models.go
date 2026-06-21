package ledger

import "time"

// EventType identifies the kind of ledger event.
type EventType string

const (
	EventAuthAccept EventType = "auth_accept"
	EventAuthReject EventType = "auth_reject"
	EventAcctStart  EventType = "accounting_start"
	EventAcctUpdate EventType = "accounting_update"
	EventAcctStop   EventType = "accounting_stop"
	EventCoA        EventType = "coa"
	EventDisconnect EventType = "disconnect"
	EventAcctOn     EventType = "accounting_on"
	EventAcctOff    EventType = "accounting_off"
)

// LedgerEntry represents a single recorded event in the ledger.
type LedgerEntry struct {
	Timestamp      string    `json:"timestamp"`
	EventType      EventType `json:"event_type"`
	OperatorID     string    `json:"operator_id"`
	MAC            string    `json:"mac"`
	SessionClass   string    `json:"session_class,omitempty"`
	PaymentType    string    `json:"payment_type,omitempty"`
	AmountSat      int       `json:"amount_sat,omitempty"`
	DurationSec    int       `json:"duration_sec,omitempty"`
	MintURL        string    `json:"mint_url,omitempty"`
	TokenHash      string    `json:"token_hash,omitempty"`
	AcctSessionID  string    `json:"acct_session_id,omitempty"`
	InputOctets    int64     `json:"input_octets,omitempty"`
	OutputOctets   int64     `json:"output_octets,omitempty"`
	SessionTime    int64     `json:"session_time,omitempty"`
	NASIP          string    `json:"nas_ip,omitempty"`
	NASID          string    `json:"nas_id,omitempty"`
	TerminateCause string    `json:"terminate_cause,omitempty"`
	ReplyMessage   string    `json:"reply_message,omitempty"`
	Metadata       string    `json:"metadata,omitempty"`
}

// RevenueReport summarizes revenue for an operator over a time range.
type RevenueReport struct {
	OperatorID       string
	NASID            string
	TotalSat         int
	TotalSessions    int
	AcceptedSessions int
	RejectedSessions int
	AverageAmount    float64
	StartTime        time.Time
	EndTime          time.Time
}
