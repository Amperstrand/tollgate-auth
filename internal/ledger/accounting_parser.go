package ledger

import (
	"strconv"
)

// ParseAccountingEvent converts FreeRADIUS exec accounting arguments into a
// LedgerEntry. All parameters arrive as strings from the FreeRADIUS exec
// module. Numeric fields that cannot be parsed are left at zero — the function
// returns nil (not an error) for invalid data so the caller can skip silently.
//
// Maps RADIUS Acct-Status-Type values:
//   - "Start" / "1"       → EventAcctStart
//   - "Stop" / "2"        → EventAcctStop
//   - "Interim-Update" / "3" → EventAcctUpdate
//   - "Accounting-On" / "7"  → EventAcctOn
//   - "Accounting-Off" / "8" → EventAcctOff
func ParseAccountingEvent(
	statusType, acctSessionID, mac, username,
	sessionTime, inputOctets, outputOctets,
	terminateCause, nasIP string,
) (*LedgerEntry, error) {

	evt, ok := mapStatusType(statusType)
	if !ok {
		return nil, nil
	}

	entry := &LedgerEntry{
		EventType:      evt,
		AcctSessionID:  acctSessionID,
		MAC:            mac,
		TerminateCause: terminateCause,
		NASIP:          nasIP,
	}

	// Parse optional numeric fields — silently zero on failure.
	if v, err := strconv.ParseInt(sessionTime, 10, 64); err == nil {
		entry.SessionTime = v
	}
	if v, err := strconv.ParseInt(inputOctets, 10, 64); err == nil {
		entry.InputOctets = v
	}
	if v, err := strconv.ParseInt(outputOctets, 10, 64); err == nil {
		entry.OutputOctets = v
	}

	return entry, nil
}

// mapStatusType maps a RADIUS Acct-Status-Type string or numeric value to an
// EventType. Returns the event and true on match, or zero-value and false for
// unknown types.
func mapStatusType(s string) (EventType, bool) {
	switch s {
	case "Start", "1":
		return EventAcctStart, true
	case "Stop", "2":
		return EventAcctStop, true
	case "Interim-Update", "3":
		return EventAcctUpdate, true
	case "Accounting-On", "7":
		return EventAcctOn, true
	case "Accounting-Off", "8":
		return EventAcctOff, true
	default:
		return "", false
	}
}
