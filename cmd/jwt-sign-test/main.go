package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	keyPath := flag.String("key", "", "Ed25519 PKCS8 PEM private key")
	pubkey := flag.String("pubkey", "test", "Client WG pubkey")
	ip := flag.String("ip", "10.66.42.42", "Allowed IP")
	serverID := flag.String("server-id", "europa-ks", "Server ID")
	ttl := flag.Int("ttl", 3600, "TTL seconds")
	flag.Parse()

	if *keyPath == "" {
		fmt.Fprintln(os.Stderr, "--key required")
		os.Exit(1)
	}

	data, err := os.ReadFile(*keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read key: %v\n", err)
		os.Exit(1)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		fmt.Fprintln(os.Stderr, "no PEM block")
		os.Exit(1)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse key: %v\n", err)
		os.Exit(1)
	}
	edKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		fmt.Fprintln(os.Stderr, "not Ed25519")
		os.Exit(1)
	}

	header := map[string]string{"alg": "EdDSA", "typ": "JWT"}
	claims := map[string]any{
		"iss":        "tollgate-auth",
		"sub":        "wg_peer",
		"pubkey":     *pubkey,
		"allowed_ip": *ip,
		"server_id":  *serverID,
		"exp":        time.Now().Add(time.Duration(*ttl) * time.Second).Unix(),
		"iat":        time.Now().Unix(),
	}

	hB, _ := json.Marshal(header)
	pB, _ := json.Marshal(claims)
	h := base64.RawURLEncoding.EncodeToString(hB)
	p := base64.RawURLEncoding.EncodeToString(pB)
	msg := h + "." + p
	sig := ed25519.Sign(edKey, []byte(msg))
	s := base64.RawURLEncoding.EncodeToString(sig)

	fmt.Println(msg + "." + s)
}
