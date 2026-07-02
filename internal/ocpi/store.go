package ocpi

import (
	"sync"
	"time"
)

// Store keeps OCPI state in memory with optional file-backed persistence.
// For PoC scale (single eMSP, single CPO, handful of chargers) this is enough.
// At real scale, swap to Postgres.
type Store struct {
	mu sync.RWMutex

	// dataDir, when non-empty, enables file-backed persistence under that path.
	// Empty means in-memory only (used by tests and any caller that does not
	// need durability). When non-empty, every mutating op also writes to disk
	// and NewStoreWithDir loads existing state at construction.
	dataDir string

	// Peer is the CPO we're peered with (single-peer PoC).
	peer *Peer

	// Tokens maps OCPI token UID → prepay record.
	tokens map[string]*PrepayRecord

	// Sessions by OCPI session ID.
	sessions map[string]*Session

	// CDRs by OCPI CDR ID.
	cdrs map[string]*CDR

	// Authorize log (most-recent-first, capped).
	authzLog []*AuthorizeEvent
}

// Persistence layout under dataDir:
//
//	cdrs/{cdr_id}.json   — one file per CDR (atomic write, latest wins)
//	prepay/{uid}.json    — one file per prepay record (atomic write)
//	authz.jsonl          — append-only audit log (one event per line)
//	{name}.json          — generic state slot for callers outside the store
//	                       (used by Server for charger state via LoadState/SaveState)
const (
	dirCDRs      = "cdrs"
	dirPrepay    = "prepay"
	fileAuthzLog = "authz.jsonl"
	stateCharger = "charger"
)

// Peer tracks the OCPI handshake state with the CPO/hub.
type Peer struct {
	OurTokenC    string // token we issue to peer after handshake
	TheirTokenB  string // token peer gave us; we send on outbound requests
	TheirURL     string // peer's version_details URL
	TheirParty   string
	TheirCountry string
	HandshakedAt time.Time
}

// PrepayRecord maps an OCPI token UID to its Cashu-funded allotment.
type PrepayRecord struct {
	UID            string     `json:"uid"`
	CashuTokenHash string     `json:"cashu_token_hash"`
	AllotmentSec   int        `json:"allotment_sec"`
	UsedSec        int        `json:"used_sec"`
	StartedAt      time.Time  `json:"started_at"`
	AmountSat      int        `json:"amount_sat"`
	MintURL        string     `json:"mint_url"`
	ContractID     string     `json:"contract_id"`
	Used           bool       `json:"used"`
	AuthorizedAt   *time.Time `json:"authorized_at,omitempty"`
}

// AuthorizeEvent is one authorize call (audit log entry).
type AuthorizeEvent struct {
	At      time.Time
	UID     string
	Allowed AuthorizationStatus
	Reason  string
	Source  string
}

const authzLogMax = 100

// NewStore returns a fresh in-memory store with no disk persistence.
// Existing tests and callers that don't need durability keep using this.
func NewStore() *Store {
	return &Store{
		tokens:   make(map[string]*PrepayRecord),
		sessions: make(map[string]*Session),
		cdrs:     make(map[string]*CDR),
	}
}

// SetPeer records the CPO peer from a successful credentials handshake.
// SetPeer(nil) clears an existing peer. Peer state is intentionally not
// persisted: a fresh handshake on restart re-establishes it.
func (s *Store) SetPeer(p *Peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peer = p
}

// GetPeer returns the CPO peer or nil if not handshaked.
func (s *Store) GetPeer() *Peer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.peer == nil {
		return nil
	}
	p := *s.peer
	return &p
}

// PutPrepay stores a prepay record and persists it when durability is enabled.
func (s *Store) PutPrepay(r *PrepayRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[r.UID] = r
	s.persistPrepayLocked(r)
}

// GetPrepay returns a prepay record by OCPI token UID.
func (s *Store) GetPrepay(uid string) (*PrepayRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.tokens[uid]
	if !ok || r == nil {
		return nil, false
	}
	out := *r
	return &out, true
}

// MarkPrepayAuthorized marks the record as having been used in an Authorize
// and persists the updated record when durability is enabled.
func (s *Store) MarkPrepayAuthorized(uid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.tokens[uid]; ok && r != nil {
		now := time.Now()
		r.AuthorizedAt = &now
		s.persistPrepayLocked(r)
	}
}

// PutSession stores an OCPI Session pushed by the CPO. Sessions are runtime
// CPO pushes and are not persisted (the auth pipeline owns session durability
// in the shared session store).
func (s *Store) PutSession(sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = sess
}

// PutCDR stores an OCPI CDR pushed by the CPO and marks any matching prepay
// record as fully used. Both mutations are persisted when durability is enabled.
func (s *Store) PutCDR(cdr *CDR) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cdrs[cdr.ID] = cdr
	s.persistCDRLocked(cdr)
	if r, ok := s.tokens[cdr.AuthID]; ok && r != nil {
		r.Used = true
		s.persistPrepayLocked(r)
	}
}

// AppendAuthz logs an authorize event (most-recent-first, capped) and appends
// it to the on-disk JSONL audit log when durability is enabled. The in-memory
// view is capped at authzLogMax; the on-disk log is append-only and uncapped
// (operator rotates if needed).
func (s *Store) AppendAuthz(ev AuthorizeEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authzLog = append([]*AuthorizeEvent{&ev}, s.authzLog...)
	if len(s.authzLog) > authzLogMax {
		s.authzLog = s.authzLog[:authzLogMax]
	}
	s.persistAuthzLocked(ev)
}

// Snapshot returns a dashboard-friendly snapshot of current state.
type Snapshot struct {
	Peer     *Peer
	Tokens   []*PrepayRecord
	Sessions []*Session
	CDRs     []*CDR
	AuthzLog []*AuthorizeEvent
}

// Snapshot returns a point-in-time copy of the store for dashboards.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Snapshot{}
	if s.peer != nil {
		p := *s.peer
		out.Peer = &p
	}
	for _, r := range s.tokens {
		cp := *r
		out.Tokens = append(out.Tokens, &cp)
	}
	for _, sess := range s.sessions {
		cp := *sess
		out.Sessions = append(out.Sessions, &cp)
	}
	for _, cdr := range s.cdrs {
		cp := *cdr
		out.CDRs = append(out.CDRs, &cp)
	}
	for _, ev := range s.authzLog {
		cp := *ev
		out.AuthzLog = append(out.AuthzLog, &cp)
	}
	return out
}
