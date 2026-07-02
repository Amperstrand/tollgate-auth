package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"time"
)

type jwtSigner struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

func loadJWTSigner(keyPath string) (*jwtSigner, error) {
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read signing key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in %s", keyPath)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	edKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not an Ed25519 key")
	}
	return &jwtSigner{priv: edKey, pub: edKey.Public().(ed25519.PublicKey)}, nil
}

func (s *jwtSigner) sign(claims map[string]any) (string, error) {
	header := map[string]string{"alg": "EdDSA", "typ": "JWT"}

	hB, _ := json.Marshal(header)
	pB, _ := json.Marshal(claims)

	h := base64.RawURLEncoding.EncodeToString(hB)
	p := base64.RawURLEncoding.EncodeToString(pB)

	msg := h + "." + p
	sig := ed25519.Sign(s.priv, []byte(msg))
	sB := base64.RawURLEncoding.EncodeToString(sig)

	return msg + "." + sB, nil
}

type vpnsServer struct {
	Endpoint   string `json:"endpoint"`
	Pubkey     string `json:"pubkey"`
	Subnet     string `json:"subnet"`
	ListenPort int    `json:"listen_port"`
}

type serverRegistry struct {
	servers map[string]vpnsServer
}

func loadServerRegistry(path string) (*serverRegistry, error) {
	if path == "" {
		return &serverRegistry{servers: make(map[string]vpnsServer)}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &serverRegistry{servers: make(map[string]vpnsServer)}, nil
		}
		return nil, err
	}
	var servers map[string]vpnsServer
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, fmt.Errorf("parse server registry: %w", err)
	}
	return &serverRegistry{servers: servers}, nil
}

func (r *serverRegistry) get(serverID string) (vpnsServer, bool) {
	s, ok := r.servers[serverID]
	return s, ok
}

func (r *serverRegistry) signSessionFor(s *jwtSigner, serverID, clientPubkey, clientIP string, expiresAt int64) (string, error) {
	return s.sign(map[string]any{
		"iss":        "tollgate-auth",
		"sub":        "wg_peer",
		"pubkey":     clientPubkey,
		"allowed_ip": clientIP,
		"server_id":  serverID,
		"exp":        expiresAt,
		"iat":        time.Now().Unix(),
	})
}
