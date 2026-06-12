package cashu

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// --- IsSpent / MarkSpent ---

func TestIsSpent_FileDoesNotExist(t *testing.T) {
	dir := t.TempDir()
	rg := NewReplayGuard(filepath.Join(dir, "nonexistent.txt"))

	if rg.IsSpent("abc123") {
		t.Error("IsSpent should return false when file does not exist")
	}
}

func TestMarkSpent_ThenIsSpent(t *testing.T) {
	dir := t.TempDir()
	rg := NewReplayGuard(filepath.Join(dir, "spent.txt"))

	hash := TokenHash("cashuAtest")
	rg.MarkSpent(hash)

	if !rg.IsSpent(hash) {
		t.Error("IsSpent should return true after MarkSpent")
	}
}

func TestIsSpent_DifferentHash(t *testing.T) {
	dir := t.TempDir()
	rg := NewReplayGuard(filepath.Join(dir, "spent.txt"))

	rg.MarkSpent("hash_a")
	if rg.IsSpent("hash_b") {
		t.Error("IsSpent should return false for a different hash")
	}
}

func TestMarkSpent_AppendsMultiple(t *testing.T) {
	dir := t.TempDir()
	rg := NewReplayGuard(filepath.Join(dir, "spent.txt"))

	rg.MarkSpent("hash_1")
	rg.MarkSpent("hash_2")
	rg.MarkSpent("hash_3")

	if !rg.IsSpent("hash_1") {
		t.Error("hash_1 should be spent")
	}
	if !rg.IsSpent("hash_2") {
		t.Error("hash_2 should be spent")
	}
	if !rg.IsSpent("hash_3") {
		t.Error("hash_3 should be spent")
	}
	if rg.IsSpent("hash_4") {
		t.Error("hash_4 should not be spent")
	}
}

// --- CheckAndMark ---

func TestCheckAndMark_FirstTimeReturnsFalse(t *testing.T) {
	dir := t.TempDir()
	rg := NewReplayGuard(filepath.Join(dir, "spent.txt"))

	alreadySpent := rg.CheckAndMark("hash_new")
	if alreadySpent {
		t.Error("CheckAndMark should return false (not spent) for first use")
	}
}

func TestCheckAndMark_SecondTimeReturnsTrue(t *testing.T) {
	dir := t.TempDir()
	rg := NewReplayGuard(filepath.Join(dir, "spent.txt"))

	rg.CheckAndMark("hash_replay")
	alreadySpent := rg.CheckAndMark("hash_replay")
	if !alreadySpent {
		t.Error("CheckAndMark should return true (already spent) on second call")
	}
}

func TestCheckAndMark_DifferentHashes(t *testing.T) {
	dir := t.TempDir()
	rg := NewReplayGuard(filepath.Join(dir, "spent.txt"))

	if rg.CheckAndMark("hash_x") {
		t.Error("first call for hash_x should return false")
	}
	if rg.CheckAndMark("hash_y") {
		t.Error("first call for hash_y should return false")
	}
	if !rg.CheckAndMark("hash_x") {
		t.Error("second call for hash_x should return true")
	}
}

func TestCheckAndMark_CreatesFileIfMissing(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "newfile.txt")

	rg := NewReplayGuard(file)
	if rg.CheckAndMark("hash_abc") {
		t.Error("first call should return false")
	}

	if _, err := os.Stat(file); os.IsNotExist(err) {
		t.Error("CheckAndMark should create the file")
	}
}

// --- Concurrent access ---

func TestCheckAndMark_ConcurrentNoDoubleSpend(t *testing.T) {
	dir := t.TempDir()
	rg := NewReplayGuard(filepath.Join(dir, "spent.txt"))

	const goroutines = 50
	var wg sync.WaitGroup
	results := make([]bool, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = rg.CheckAndMark("concurrent_hash")
		}(i)
	}
	wg.Wait()

	// Exactly one goroutine should get false (not spent), all others true
	notSpentCount := 0
	for _, r := range results {
		if !r {
			notSpentCount++
		}
	}
	if notSpentCount != 1 {
		t.Errorf("expected exactly 1 'not spent' result, got %d (race condition?)", notSpentCount)
	}
}

func TestCheckAndMark_ConcurrentDifferentHashes(t *testing.T) {
	dir := t.TempDir()
	rg := NewReplayGuard(filepath.Join(dir, "spent.txt"))

	const goroutines = 20
	var wg sync.WaitGroup
	results := make([]bool, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			hash := TokenHash(string(rune('A' + idx)))
			results[idx] = rg.CheckAndMark(hash)
		}(i)
	}
	wg.Wait()

	// All different hashes — all should return false (not spent)
	for i, r := range results {
		if r {
			t.Errorf("goroutine %d: different hash should return false (not spent)", i)
		}
	}
}

// --- LogToken / LogTokenWithError ---

func TestLogToken_WritesJSONL(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "tokens.jsonl")

	td := &TokenData{
		Mint:   "https://testnut.cashu.space",
		Amount: 8,
		Unit:   "sat",
	}

	LogToken("cashuAtest", td, "guest-abc", true, logFile)

	f, err := os.Open(logFile)
	if err != nil {
		t.Fatalf("log file not created: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("log file is empty")
	}

	var entry map[string]interface{}
	if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if entry["guest"] != "guest-abc" {
		t.Errorf("guest = %v, want guest-abc", entry["guest"])
	}
	if entry["accepted"] != true {
		t.Errorf("accepted = %v, want true", entry["accepted"])
	}
	if entry["mint"] != "https://testnut.cashu.space" {
		t.Errorf("mint = %v, want https://testnut.cashu.space", entry["mint"])
	}
	if entry["amount"] != float64(8) {
		t.Errorf("amount = %v, want 8", entry["amount"])
	}
	// Should have a hash field
	if _, ok := entry["hash"]; !ok {
		t.Error("missing 'hash' field")
	}
	// Should NOT have an error field
	if _, ok := entry["error"]; ok {
		t.Error("should not have 'error' field for LogToken")
	}
}

func TestLogTokenWithError_IncludesError(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "tokens.jsonl")

	td := &TokenData{
		Mint:   "https://testnut.cashu.space",
		Amount: 4,
		Unit:   "sat",
	}

	LogTokenWithError("cashuAtest", td, "guest-xyz", false, "token already spent", logFile)

	f, err := os.Open(logFile)
	if err != nil {
		t.Fatalf("log file not created: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("log file is empty")
	}

	var entry map[string]interface{}
	if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if entry["error"] != "token already spent" {
		t.Errorf("error = %v, want 'token already spent'", entry["error"])
	}
	if entry["accepted"] != false {
		t.Errorf("accepted = %v, want false", entry["accepted"])
	}
}

func TestLogToken_AppendsMultiple(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "tokens.jsonl")

	td := &TokenData{Mint: "https://testnut.cashu.space", Amount: 1, Unit: "sat"}
	LogToken("token1", td, "g1", true, logFile)
	LogToken("token2", td, "g2", true, logFile)

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}

	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 2 {
		t.Errorf("expected 2 log lines, got %d", lines)
	}
}

func TestLogToken_EmptyErrorMsg_NoErrorField(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "tokens.jsonl")

	td := &TokenData{Mint: "https://testnut.cashu.space", Amount: 1, Unit: "sat"}
	LogTokenWithError("token", td, "g1", true, "", logFile)

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}

	var entry map[string]interface{}
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := entry["error"]; ok {
		t.Error("empty error message should not produce 'error' field")
	}
}
