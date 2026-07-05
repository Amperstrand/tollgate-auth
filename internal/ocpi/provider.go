package ocpi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Provider struct {
	Npub      string    `json:"npub"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type TokenProvider struct {
	TokenHash   string    `json:"token_hash"`
	ProviderNpub string   `json:"provider_npub"`
	Amount      int       `json:"amount"`
	Unit        string    `json:"unit"`
	CreatedAt   time.Time `json:"created_at"`
}

type ProviderStore struct {
	mu       sync.RWMutex
	dataDir  string
	providers map[string]*Provider
	tokenMap  map[string]*TokenProvider
}

func NewProviderStore() *ProviderStore {
	return &ProviderStore{
		providers: make(map[string]*Provider),
		tokenMap:  make(map[string]*TokenProvider),
	}
}

func NewProviderStoreWithDir(dataDir string) (*ProviderStore, error) {
	ps := NewProviderStore()
	ps.dataDir = dataDir

	dir := filepath.Join(dataDir, "providers")
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, err
	}
	tdir := filepath.Join(dataDir, "token_providers")
	if err := os.MkdirAll(tdir, 0750); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var p Provider
		if json.Unmarshal(data, &p) == nil && p.Npub != "" {
			ps.providers[p.Npub] = &p
		}
	}

	tentries, err := os.ReadDir(tdir)
	if err != nil {
		return nil, err
	}
	for _, e := range tentries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(tdir, e.Name()))
		if err != nil {
			continue
		}
		var tp TokenProvider
		if json.Unmarshal(data, &tp) == nil && tp.TokenHash != "" {
			ps.tokenMap[tp.TokenHash] = &tp
		}
	}

	return ps, nil
}

func (ps *ProviderStore) PutProvider(p *Provider) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	ps.providers[p.Npub] = p
	ps.persistProviderLocked(p)
}

func (ps *ProviderStore) GetProvider(npub string) (*Provider, bool) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	p, ok := ps.providers[npub]
	if !ok {
		return nil, false
	}
	out := *p
	return &out, true
}

func (ps *ProviderStore) PutTokenProvider(tp *TokenProvider) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if tp.CreatedAt.IsZero() {
		tp.CreatedAt = time.Now()
	}
	ps.tokenMap[tp.TokenHash] = tp
	ps.persistTokenProviderLocked(tp)
}

func (ps *ProviderStore) GetTokenProvider(tokenHash string) (*TokenProvider, bool) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	tp, ok := ps.tokenMap[tokenHash]
	if !ok {
		return nil, false
	}
	out := *tp
	return &out, true
}

func (ps *ProviderStore) persistProviderLocked(p *Provider) {
	if ps.dataDir == "" {
		return
	}
	path := filepath.Join(ps.dataDir, "providers", p.Npub+".json")
	data, _ := json.MarshalIndent(p, "", "  ")
	_ = atomicWriteFile(path, data)
}

func (ps *ProviderStore) persistTokenProviderLocked(tp *TokenProvider) {
	if ps.dataDir == "" {
		return
	}
	path := filepath.Join(ps.dataDir, "token_providers", tp.TokenHash+".json")
	data, _ := json.MarshalIndent(tp, "", "  ")
	_ = atomicWriteFile(path, data)
}

func atomicWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type ProviderRegisterRequest struct {
	Npub string `json:"npub"`
	Name string `json:"name"`
}

func (s *Server) HandleProviderRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, Err(StatusUnknownMethod, "POST required"))
		return
	}
	var body ProviderRegisterRequest
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil {
		writeJSON(w, Err(StatusClientError, "cannot read body"))
		return
	}
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		writeJSON(w, Err(StatusClientError, "invalid JSON"))
		return
	}
	if body.Npub == "" {
		writeJSON(w, Err(StatusClientError, "npub is required"))
		return
	}

	s.providers.PutProvider(&Provider{
		Npub: body.Npub,
		Name: body.Name,
	})
	slog.Info("provider registered", "npub", body.Npub, "name", body.Name)
	writeJSON(w, OK(map[string]string{
		"status": "registered",
		"npub":   body.Npub,
		"name":   body.Name,
	}))
}
