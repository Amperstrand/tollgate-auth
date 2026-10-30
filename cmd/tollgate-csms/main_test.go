package main

import (
	"testing"
	"time"

	"github.com/lorenzodonini/ocpp-go/ocpp1.6/core"
	"github.com/lorenzodonini/ocpp-go/ocpp1.6/types"
)

// TestOnStartTransaction_TxnIDNotReused reproduces issue #22 (P2): transaction
// IDs were derived from len(activeTxns)+1001, so after a stop the next start
// reused a still-live ID (or one whose CDR was already pushed), mis-attributing
// meter/CDR data. IDs must come from a monotonic counter.
func TestOnStartTransaction_TxnIDNotReused(t *testing.T) {
	// Dead EMSP URL: pushes are fire-and-forget and logged, never fatal.
	h := newCSMSHandler("http://127.0.0.1:1")

	start := func() int {
		conf, err := h.OnStartTransaction("cp-1", &core.StartTransactionRequest{
			ConnectorId: 1,
			IdTag:       "tagA",
			MeterStart:  0,
			Timestamp:   types.NewDateTime(time.Now()),
		})
		if err != nil {
			t.Fatalf("OnStartTransaction: %v", err)
		}
		return conf.TransactionId
	}

	first := start()
	second := start()
	if first != 1001 || second != 1002 {
		t.Fatalf("baseline IDs = %d,%d; want 1001,1002", first, second)
	}

	// Stop the first transaction, then start a third: with the old
	// len(activeTxns)+1001 derivation this yields 1002 again, colliding with
	// the still-active second transaction.
	if _, err := h.OnStopTransaction("cp-1", &core.StopTransactionRequest{
		TransactionId: first,
		MeterStop:     1000,
		Timestamp:     types.NewDateTime(time.Now()),
	}); err != nil {
		t.Fatalf("OnStopTransaction: %v", err)
	}

	third := start()
	if third == second {
		t.Fatalf("txnID %d reused for a new transaction while %d is still active", third, second)
	}
	if third != 1003 {
		t.Fatalf("third txnID = %d; want 1003 (monotonic)", third)
	}

	// The still-active second transaction must survive the churn untouched.
	h.mu.Lock()
	_, ok := h.activeTxns[second]
	h.mu.Unlock()
	if !ok {
		t.Fatalf("active transaction %d lost after start/stop churn", second)
	}
}
