package verify

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerify_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"amount":8}`)
	}))
	defer srv.Close()

	got, err := NewClient(srv.URL).Verify("cashuBtest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Amount != 8 {
		t.Fatalf("amount = %d, want 8", got.Amount)
	}
}

func TestVerify_Spent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"ok":false,"code":"payment-error-token-spent","error":"already"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL).Verify("cashuBtest")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	re, ok := err.(*RejectedError)
	if !ok {
		t.Fatalf("expected *RejectedError, got %T: %v", err, err)
	}
	if !re.IsSpent() {
		t.Errorf("IsSpent = false, want true (code=%s)", re.Code)
	}
}

func TestVerify_EmptyToken(t *testing.T) {
	if _, err := NewClient("").Verify(""); err == nil {
		t.Fatal("expected error for empty token")
	}
}
