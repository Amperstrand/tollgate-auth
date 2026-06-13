package testtoken

import (
	"testing"

	"tollgate-auth/internal/cashu"
)

func TestV4Token(t *testing.T) {
	tok := V4Token(8)
	td, err := cashu.DecodeToken(tok)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if td.Amount != 8 {
		t.Errorf("amount = %d, want 8", td.Amount)
	}
	if td.Mint != TestMint {
		t.Errorf("mint = %q, want %q", td.Mint, TestMint)
	}
}

func TestV4TokenAmounts(t *testing.T) {
	for _, amt := range []int{1, 2, 4, 8, 16, 32, 64, 100, 500, 1000} {
		td, err := cashu.DecodeToken(V4Token(amt))
		if err != nil {
			t.Fatalf("amount %d: decode failed: %v", amt, err)
		}
		if td.Amount != amt {
			t.Errorf("amount %d: got %d", amt, td.Amount)
		}
	}
}

func TestV4TokenZeroAmount(t *testing.T) {
	_, err := cashu.DecodeToken(V4TokenZeroAmount())
	if err == nil {
		t.Fatal("expected error for zero-amount token")
	}
}

func TestV4TokenDLEQSplit(t *testing.T) {
	first, rest := V4TokenDLEQSplit()
	full := first + rest
	td, err := cashu.DecodeToken(full)
	if err != nil {
		t.Fatalf("reassembled split token decode failed: %v", err)
	}
	if td.Amount != 8 {
		t.Errorf("amount = %d, want 8", td.Amount)
	}
	if len(first) != 200 {
		t.Errorf("first part len = %d, want 200", len(first))
	}
}

func TestV3Token(t *testing.T) {
	tok := V3Token(8)
	td, err := cashu.DecodeToken(tok)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if td.Amount != 8 {
		t.Errorf("amount = %d, want 8", td.Amount)
	}
}

func TestV4TokenWithCustomMint(t *testing.T) {
	mint := "https://mytestmint.example.com"
	tok := V4TokenWithMint(8, mint)
	td, err := cashu.DecodeToken(tok)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if td.Mint != mint {
		t.Errorf("mint = %q, want %q", td.Mint, mint)
	}
}

func TestLNURLwCode(t *testing.T) {
	code := LNURLwCode()
	if len(code) < 10 {
		t.Errorf("LNURLw code too short: %d chars", len(code))
	}
	if code[:6] != "lnurlw" {
		t.Errorf("prefix = %q, want lnurlw", code[:6])
	}
}

func TestLNURLwUpper(t *testing.T) {
	code := LNURLwUpper()
	if code[:6] != "LNURLW" {
		t.Errorf("prefix = %q, want LNURLW", code[:6])
	}
}

func TestInvalidTokens(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func() string
	}{
		{"garbage", InvalidGarbage},
		{"bad prefix", InvalidPrefix},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cashu.DecodeToken(tc.fn())
			if err == nil {
				t.Fatal("expected decode error")
			}
		})
	}
}

func TestShellInjection(t *testing.T) {
	tok := ShellInjection()
	if tok[:6] != "cashuB" {
		t.Fatal("missing cashuB prefix")
	}
	if !containsAny(tok, ";") == false {
		t.Error("shell injection token should contain semicolon in encoded data")
	}
}

func containsAny(s, chars string) bool {
	for _, c := range chars {
		for _, sc := range s {
			if c == sc {
				return true
			}
		}
	}
	return false
}
