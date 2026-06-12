package ledger

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Ledger is a SQLite-backed accounting ledger.
type Ledger struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS ledger_entries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp TEXT NOT NULL,
    event_type TEXT NOT NULL,
    operator_id TEXT NOT NULL DEFAULT 'default',
    mac TEXT NOT NULL,
    session_class TEXT,
    payment_type TEXT,
    amount_sat INTEGER,
    duration_sec INTEGER,
    mint_url TEXT,
    token_hash TEXT,
    acct_session_id TEXT,
    input_octets INTEGER,
    output_octets INTEGER,
    session_time INTEGER,
    nas_ip TEXT,
    terminate_cause TEXT,
    reply_message TEXT,
    metadata TEXT
);

CREATE INDEX IF NOT EXISTS idx_ledger_mac ON ledger_entries(mac);
CREATE INDEX IF NOT EXISTS idx_ledger_operator ON ledger_entries(operator_id);
CREATE INDEX IF NOT EXISTS idx_ledger_timestamp ON ledger_entries(timestamp);
CREATE INDEX IF NOT EXISTS idx_ledger_token_hash ON ledger_entries(token_hash);
CREATE INDEX IF NOT EXISTS idx_ledger_event_type ON ledger_entries(event_type);
`

// OpenLedger opens (or creates) the SQLite ledger at the given path.
// Creates the schema if it doesn't exist.
func OpenLedger(path string) (*Ledger, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("ledger: open database: %w", err)
	}

	// Single-writer: serialize all access through one connection.
	db.SetMaxOpenConns(1)

	// Enable WAL mode for concurrent read access.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("ledger: enable WAL: %w", err)
	}

	// Enable foreign keys.
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("ledger: enable foreign keys: %w", err)
	}

	// Set busy timeout so concurrent writers retry instead of failing immediately.
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("ledger: set busy timeout: %w", err)
	}

	// Create schema.
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("ledger: create schema: %w", err)
	}

	return &Ledger{db: db}, nil
}

// Close closes the underlying database connection.
func (l *Ledger) Close() error {
	return l.db.Close()
}

// insertEntry is the shared INSERT logic for all record methods.
func (l *Ledger) insertEntry(entry LedgerEntry) error {
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	_, err := l.db.Exec(`
		INSERT INTO ledger_entries (
			timestamp, event_type, operator_id, mac,
			session_class, payment_type, amount_sat, duration_sec,
			mint_url, token_hash, acct_session_id,
			input_octets, output_octets, session_time,
			nas_ip, terminate_cause, reply_message, metadata
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.Timestamp, string(entry.EventType), entry.OperatorID, entry.MAC,
		entry.SessionClass, entry.PaymentType, entry.AmountSat, entry.DurationSec,
		entry.MintURL, entry.TokenHash, entry.AcctSessionID,
		entry.InputOctets, entry.OutputOctets, entry.SessionTime,
		entry.NASIP, entry.TerminateCause, entry.ReplyMessage, entry.Metadata,
	)
	if err != nil {
		return fmt.Errorf("ledger: insert entry: %w", err)
	}
	return nil
}

// RecordAuth records an authentication event (accept or reject).
func (l *Ledger) RecordAuth(entry LedgerEntry) error {
	return l.insertEntry(entry)
}

// RecordAccounting records an accounting event (start/update/stop).
func (l *Ledger) RecordAccounting(entry LedgerEntry) error {
	return l.insertEntry(entry)
}

// RecordCoA records a CoA or disconnect event.
func (l *Ledger) RecordCoA(entry LedgerEntry) error {
	return l.insertEntry(entry)
}

// scanEntries scans multiple rows into a slice of LedgerEntry.
func scanEntries(rows *sql.Rows) ([]LedgerEntry, error) {
	var entries []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		var eventType string
		err := rows.Scan(
			&e.ID, &e.Timestamp, &eventType, &e.OperatorID, &e.MAC,
			&e.SessionClass, &e.PaymentType, &e.AmountSat, &e.DurationSec,
			&e.MintURL, &e.TokenHash, &e.AcctSessionID,
			&e.InputOctets, &e.OutputOctets, &e.SessionTime,
			&e.NASIP, &e.TerminateCause, &e.ReplyMessage, &e.Metadata,
		)
		if err != nil {
			return nil, fmt.Errorf("ledger: scan entry: %w", err)
		}
		e.EventType = EventType(eventType)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger: rows error: %w", err)
	}
	return entries, nil
}

// QueryByMAC returns all ledger entries for a MAC address since the given time.
func (l *Ledger) QueryByMAC(mac string, since time.Time) ([]LedgerEntry, error) {
	rows, err := l.db.Query(`
		SELECT id, timestamp, event_type, operator_id, mac,
			session_class, payment_type, amount_sat, duration_sec,
			mint_url, token_hash, acct_session_id,
			input_octets, output_octets, session_time,
			nas_ip, terminate_cause, reply_message, metadata
		FROM ledger_entries
		WHERE mac = ? AND timestamp >= ?
		ORDER BY timestamp DESC`,
		mac, since.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("ledger: query by MAC: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

// QueryByOperator returns all ledger entries for an operator since the given time.
func (l *Ledger) QueryByOperator(operatorID string, since time.Time) ([]LedgerEntry, error) {
	rows, err := l.db.Query(`
		SELECT id, timestamp, event_type, operator_id, mac,
			session_class, payment_type, amount_sat, duration_sec,
			mint_url, token_hash, acct_session_id,
			input_octets, output_octets, session_time,
			nas_ip, terminate_cause, reply_message, metadata
		FROM ledger_entries
		WHERE operator_id = ? AND timestamp >= ?
		ORDER BY timestamp DESC`,
		operatorID, since.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("ledger: query by operator: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

// GetActiveSession returns the most recent auth_accept for a MAC that hasn't been stopped.
// Used for session reconnection checks.
func (l *Ledger) GetActiveSession(mac string) (*LedgerEntry, error) {
	row := l.db.QueryRow(`
		SELECT id, timestamp, event_type, operator_id, mac,
			session_class, payment_type, amount_sat, duration_sec,
			mint_url, token_hash, acct_session_id,
			input_octets, output_octets, session_time,
			nas_ip, terminate_cause, reply_message, metadata
		FROM ledger_entries
		WHERE mac = ? AND event_type = ?
		ORDER BY timestamp DESC
		LIMIT 1`,
		mac, string(EventAuthAccept),
	)

	var e LedgerEntry
	var eventType string
	err := row.Scan(
		&e.ID, &e.Timestamp, &eventType, &e.OperatorID, &e.MAC,
		&e.SessionClass, &e.PaymentType, &e.AmountSat, &e.DurationSec,
		&e.MintURL, &e.TokenHash, &e.AcctSessionID,
		&e.InputOctets, &e.OutputOctets, &e.SessionTime,
		&e.NASIP, &e.TerminateCause, &e.ReplyMessage, &e.Metadata,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: get active session: %w", err)
	}
	e.EventType = EventType(eventType)

	// Check for a corresponding accounting_stop with the same token_hash.
	if e.TokenHash != "" {
		var stopCount int
		err = l.db.QueryRow(`
			SELECT COUNT(*) FROM ledger_entries
			WHERE mac = ? AND event_type = ? AND token_hash = ?`,
			mac, string(EventAcctStop), e.TokenHash,
		).Scan(&stopCount)
		if err != nil {
			return nil, fmt.Errorf("ledger: check stop: %w", err)
		}
		if stopCount > 0 {
			return nil, nil // session has been stopped
		}
	}

	return &e, nil
}

// RevenueSummary returns a revenue report for an operator over a time range.
func (l *Ledger) RevenueSummary(operatorID string, start, end time.Time) (*RevenueReport, error) {
	startStr := start.UTC().Format(time.RFC3339)
	endStr := end.UTC().Format(time.RFC3339)

	report := &RevenueReport{
		OperatorID: operatorID,
		StartTime:  start,
		EndTime:    end,
	}

	// Total sessions (auth events).
	err := l.db.QueryRow(`
		SELECT COUNT(*) FROM ledger_entries
		WHERE operator_id = ? AND timestamp >= ? AND timestamp <= ?
			AND event_type IN (?, ?)`,
		operatorID, startStr, endStr,
		string(EventAuthAccept), string(EventAuthReject),
	).Scan(&report.TotalSessions)
	if err != nil {
		return nil, fmt.Errorf("ledger: count total sessions: %w", err)
	}

	// Accepted sessions and sum.
	err = l.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(amount_sat), 0) FROM ledger_entries
		WHERE operator_id = ? AND timestamp >= ? AND timestamp <= ?
			AND event_type = ?`,
		operatorID, startStr, endStr,
		string(EventAuthAccept),
	).Scan(&report.AcceptedSessions, &report.TotalSat)
	if err != nil {
		return nil, fmt.Errorf("ledger: count accepted: %w", err)
	}

	// Rejected sessions.
	err = l.db.QueryRow(`
		SELECT COUNT(*) FROM ledger_entries
		WHERE operator_id = ? AND timestamp >= ? AND timestamp <= ?
			AND event_type = ?`,
		operatorID, startStr, endStr,
		string(EventAuthReject),
	).Scan(&report.RejectedSessions)
	if err != nil {
		return nil, fmt.Errorf("ledger: count rejected: %w", err)
	}

	// Average amount.
	if report.AcceptedSessions > 0 {
		report.AverageAmount = float64(report.TotalSat) / float64(report.AcceptedSessions)
	}

	return report, nil
}
