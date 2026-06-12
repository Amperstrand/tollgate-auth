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
)

// LedgerEntry represents a single recorded event in the ledger.
type LedgerEntry struct {
	ID             int64
	Timestamp      string // ISO 8601
	EventType      EventType
	OperatorID     string
	MAC            string
	SessionClass   string // RADIUS Class attribute (HMAC-signed)
	PaymentType    string // "cashu", "lnurlw", "delegated"
	AmountSat      int
	DurationSec    int
	MintURL        string
	TokenHash      string
	AcctSessionID  string // NAS-provided Acct-Session-Id
	InputOctets    int64
	OutputOctets   int64
	SessionTime    int64
	NASIP          string
	TerminateCause string
	ReplyMessage   string
	Metadata       string // JSON blob for extra fields
}

// RevenueReport summarizes revenue for an operator over a time range.
type RevenueReport struct {
	OperatorID       string
	TotalSat         int
	TotalSessions    int
	AcceptedSessions int
	RejectedSessions int
	AverageAmount    float64
	StartTime        time.Time
	EndTime          time.Time
}
