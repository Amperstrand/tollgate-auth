package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestEd25519Key(t *testing.T, dir string) string {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pemData := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	path := filepath.Join(dir, "test.key")
	if err := os.WriteFile(path, pemData, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func parseJWTClaims(t *testing.T, token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	return claims
}

func verifyJWTSignature(t *testing.T, token string, pub ed25519.PublicKey) {
	parts := strings.Split(token, ".")
	signed := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	if !ed25519.Verify(pub, []byte(signed), sig) {
		t.Fatal("signature verification failed")
	}
}

func TestJWTSigner_LoadAndSign(t *testing.T) {
	dir := t.TempDir()
	keyPath := writeTestEd25519Key(t, dir)

	signer, err := loadJWTSigner(keyPath)
	if err != nil {
		t.Fatalf("loadJWTSigner: %v", err)
	}

	claims := map[string]any{
		"iss":        "tollgate-auth",
		"sub":        "wg_peer",
		"pubkey":     "testpubkey123",
		"allowed_ip": "10.0.0.5",
		"server_id":  "europa-ks",
		"exp":        time.Now().Add(time.Hour).Unix(),
		"iat":        time.Now().Unix(),
	}

	token, err := signer.sign(claims)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}

	verifyJWTSignature(t, token, signer.pub)

	parsed := parseJWTClaims(t, token)
	if parsed["pubkey"] != "testpubkey123" {
		t.Errorf("wrong pubkey: %v", parsed["pubkey"])
	}
	if parsed["server_id"] != "europa-ks" {
		t.Errorf("wrong server_id: %v", parsed["server_id"])
	}
}

func TestJWTSigner_InvalidKeyFile(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "bad.key")
	os.WriteFile(badPath, []byte("not a key"), 0600)

	_, err := loadJWTSigner(badPath)
	if err == nil {
		t.Fatal("expected error for invalid key")
	}
}

func TestJWTSigner_MissingKeyFile(t *testing.T) {
	_, err := loadJWTSigner("/nonexistent/key")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestServerRegistry_Load(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "servers.json")
	servers := map[string]vpnsServer{
		"europa-ks": {
			Endpoint:   "66.92.204.236:51820",
			Pubkey:     "testpubkey",
			Subnet:     "10.66.42.0/24",
			ListenPort: 51820,
		},
	}
	data, _ := json.Marshal(servers)
	os.WriteFile(regPath, data, 0644)

	reg, err := loadServerRegistry(regPath)
	if err != nil {
		t.Fatalf("loadServerRegistry: %v", err)
	}

	srv, ok := reg.get("europa-ks")
	if !ok {
		t.Fatal("server not found")
	}
	if srv.Endpoint != "66.92.204.236:51820" {
		t.Errorf("wrong endpoint: %s", srv.Endpoint)
	}
	if srv.Subnet != "10.66.42.0/24" {
		t.Errorf("wrong subnet: %s", srv.Subnet)
	}
}

func TestServerRegistry_MissingFile(t *testing.T) {
	reg, err := loadServerRegistry("/nonexistent/servers.json")
	if err != nil {
		t.Fatalf("should not error on missing file: %v", err)
	}
	if len(reg.servers) != 0 {
		t.Errorf("expected empty registry")
	}
}

func TestServerRegistry_UnknownServer(t *testing.T) {
	reg := &serverRegistry{servers: make(map[string]vpnsServer)}
	_, ok := reg.get("nonexistent")
	if ok {
		t.Fatal("expected false for unknown server")
	}
}

func TestServerRegistry_SignSessionFor(t *testing.T) {
	dir := t.TempDir()
	keyPath := writeTestEd25519Key(t, dir)
	signer, err := loadJWTSigner(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	reg := &serverRegistry{servers: make(map[string]vpnsServer)}
	exp := time.Now().Add(time.Hour).Unix()

	token, err := reg.signSessionFor(signer, "europa-ks", "clientPubKey123", "10.0.0.5", exp)
	if err != nil {
		t.Fatalf("signSessionFor: %v", err)
	}

	verifyJWTSignature(t, token, signer.pub)
	claims := parseJWTClaims(t, token)

	if claims["server_id"] != "europa-ks" {
		t.Errorf("wrong server_id: %v", claims["server_id"])
	}
	if claims["pubkey"] != "clientPubKey123" {
		t.Errorf("wrong pubkey: %v", claims["pubkey"])
	}
	if claims["allowed_ip"] != "10.0.0.5" {
		t.Errorf("wrong allowed_ip: %v", claims["allowed_ip"])
	}
	if claims["iss"] != "tollgate-auth" {
		t.Errorf("wrong iss: %v", claims["iss"])
	}
}

func TestWGConnectRequest_Unmarshal(t *testing.T) {
	jsonStr := `{"token":"cashuA123","pubkey":"wgpubkey","server_id":"europa-ks"}`
	var req wgConnectRequest
	if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
		t.Fatal(err)
	}
	if req.ServerID != "europa-ks" {
		t.Errorf("wrong server_id: %s", req.ServerID)
	}
	if req.Token != "cashuA123" {
		t.Errorf("wrong token: %s", req.Token)
	}
}

func TestWGConnectResponse_IncludesJWT(t *testing.T) {
	resp := wgConnectResponse{
		ClientIP:     "10.0.0.5",
		ServerPubkey: "serverpub",
		ExpiresAt:    1234567890,
		Endpoint:     "vpn.example.com:51820",
		JWT:          "eyJ.header.payload.sig",
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)
	if m["jwt"] == nil {
		t.Error("JWT field missing from JSON")
	}
	if m["endpoint"] == nil {
		t.Error("endpoint field missing from JSON")
	}
}
