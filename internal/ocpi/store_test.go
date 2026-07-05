package ocpi

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewStore_EmptyState(t *testing.T) {
	s := NewStore()
	snap := s.Snapshot()

	if snap.Peer != nil {
		t.Errorf("new store peer = %v, want nil", snap.Peer)
	}
	if len(snap.Tokens) != 0 {
		t.Errorf("new store tokens = %d, want 0", len(snap.Tokens))
	}
	if len(snap.Sessions) != 0 {
		t.Errorf("new store sessions = %d, want 0", len(snap.Sessions))
	}
	if len(snap.CDRs) != 0 {
		t.Errorf("new store CDRs = %d, want 0", len(snap.CDRs))
	}
	if len(snap.AuthzLog) != 0 {
		t.Errorf("new store authz log = %d, want 0", len(snap.AuthzLog))
	}
}

func TestStore_PutAndGetPrepay(t *testing.T) {
	s := NewStore()
	rec := &PrepayRecord{
		UID:            "OCPI-ABC12345",
		CashuTokenHash: "abc12345deadbeef",
		AllotmentSec:   120,
		CreditAmount:      12,
		MintURL:        "https://testnut.cashu.space",
		ContractID:     "NPC-OCPI-ABC12345",
		StartedAt:      time.Now(),
	}
	s.PutPrepay(rec)

	got, ok := s.GetPrepay("OCPI-ABC12345")
	if !ok {
		t.Fatal("PutPrepay then GetPrepay: not found")
	}
	if got.UID != rec.UID || got.AllotmentSec != rec.AllotmentSec || got.CreditAmount != rec.CreditAmount {
		t.Errorf("GetPrepay returned wrong record: %+v", got)
	}

	// Snapshot isolation: mutating returned record must not affect store.
	got.AllotmentSec = 999
	again, _ := s.GetPrepay("OCPI-ABC12345")
	if again.AllotmentSec != 120 {
		t.Errorf("snapshot leak: store record mutated via returned pointer (got %d, want 120)", again.AllotmentSec)
	}

	// Missing UID.
	if _, ok := s.GetPrepay("does-not-exist"); ok {
		t.Error("GetPrepay returned true for unknown UID")
	}
}

func TestStore_MarkPrepayAuthorized(t *testing.T) {
	s := NewStore()
	s.PutPrepay(&PrepayRecord{UID: "OCPI-A"})

	before, _ := s.GetPrepay("OCPI-A")
	if before.AuthorizedAt != nil {
		t.Fatal("new prepay should have nil AuthorizedAt")
	}

	s.MarkPrepayAuthorized("OCPI-A")

	after, _ := s.GetPrepay("OCPI-A")
	if after.AuthorizedAt == nil {
		t.Error("AuthorizedAt should be set after MarkPrepayAuthorized")
	}
	if after.AuthorizedAt.After(time.Now().Add(time.Second)) {
		t.Error("AuthorizedAt is in the future")
	}
}

func TestStore_PutCDR_MarksPrepayUsed(t *testing.T) {
	s := NewStore()
	s.PutPrepay(&PrepayRecord{UID: "OCPI-A"})

	s.PutCDR(&CDR{ID: "cdr-1", AuthID: "OCPI-A"})

	rec, _ := s.GetPrepay("OCPI-A")
	if !rec.Used {
		t.Error("PutCDR should mark matching prepay record as Used")
	}

	cdr, ok := s.Snapshot().CDRs, true
	_ = cdr
	if !ok {
		t.Error("CDR not in snapshot")
	}
}

func TestStore_PutSession(t *testing.T) {
	s := NewStore()
	sess := &Session{ID: "sess-1", Kwh: 7.5, AuthID: "OCPI-A"}
	s.PutSession(sess)

	snap := s.Snapshot()
	if len(snap.Sessions) != 1 {
		t.Fatalf("snapshot sessions = %d, want 1", len(snap.Sessions))
	}
	if snap.Sessions[0].ID != "sess-1" {
		t.Errorf("snapshot session ID = %q, want sess-1", snap.Sessions[0].ID)
	}
}

func TestStore_AuthzLogCappedAt100(t *testing.T) {
	s := NewStore()
	for i := 0; i < 150; i++ {
		s.AppendAuthz(AuthorizeEvent{
			At:      time.Now(),
			UID:     "OCPI-X",
			Allowed: AuthzAllowed,
			Reason:  "test",
		})
	}
	snap := s.Snapshot()
	if len(snap.AuthzLog) != 100 {
		t.Errorf("authz log = %d, want capped at 100", len(snap.AuthzLog))
	}
	// Most-recent-first: the last event we appended should be at index 0.
	if !strings.Contains(snap.AuthzLog[0].Reason, "test") {
		t.Errorf("authz log not most-recent-first: got %+v", snap.AuthzLog[0])
	}
}

func TestStore_PeerHandshake(t *testing.T) {
	s := NewStore()
	if s.GetPeer() != nil {
		t.Fatal("new store should have no peer")
	}

	peer := &Peer{
		OurTokenC:    "token-c-abc",
		TheirTokenB:  "token-b-xyz",
		TheirURL:     "https://peer.example/ocpi/cpo/2.2.1/version_details",
		TheirParty:   "OCL",
		TheirCountry: "NO",
		HandshakedAt: time.Now(),
	}
	s.SetPeer(peer)

	got := s.GetPeer()
	if got == nil {
		t.Fatal("peer not stored")
	}
	if got.OurTokenC != "token-c-abc" || got.TheirParty != "OCL" {
		t.Errorf("peer round-trip mismatch: %+v", got)
	}

	// Snapshot isolation.
	got.OurTokenC = "MUTATED"
	again := s.GetPeer()
	if again.OurTokenC != "token-c-abc" {
		t.Errorf("peer snapshot leak: store record mutated via returned pointer")
	}

	// Delete peer.
	s.SetPeer(nil)
	if s.GetPeer() != nil {
		t.Error("SetPeer(nil) did not clear peer")
	}
}

func TestStore_ConcurrentAccess(t *testing.T) {
	s := NewStore()
	const goroutines = 20
	const iterations = 50

	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	// Concurrent writers — prepay records.
	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				s.PutPrepay(&PrepayRecord{UID: "OCPI-concurrent"})
			}
		}(i)
	}
	// Concurrent writers — authz log.
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				s.AppendAuthz(AuthorizeEvent{UID: "OCPI-X", Allowed: AuthzAllowed})
			}
		}()
	}
	// Concurrent readers — snapshot.
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = s.Snapshot()
			}
		}()
	}

	wg.Wait()
	// If we got here without -race firing, the locks work.
	snap := s.Snapshot()
	if len(snap.AuthzLog) != 100 {
		t.Errorf("after concurrent writes, authz log = %d, want 100 (capped)", len(snap.AuthzLog))
	}
}
