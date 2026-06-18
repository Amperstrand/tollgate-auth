// cmd/tollgate-wg connects to a tollgate daemon's WireGuard endpoint, exchanging
// a Cashu ecash token for a WireGuard client configuration.
//
// Usage:
//
//	tollgate-wg <cashu-token> [--server <hostname>]
//
// The server defaults to localhost (TOLLGATE_SERVER env var overrides). The
// daemon API runs on port 8091; the WireGuard endpoint uses port 51820.
//
// Output (stdout) is a complete wg-quick config. Save it and run:
//
//	sudo wg-quick up wg-tollgate
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultServer = "localhost"
	apiPort       = "8091"
	wgPort        = "51820"
	httpTimeout   = 30 * time.Second
)

type connectRequest struct {
	Token  string `json:"token"`
	Pubkey string `json:"pubkey"`
}

type connectResponse struct {
	ClientIP       string `json:"client_ip"`
	SessionTimeout int    `json:"session_timeout"`
	ServerPubkey   string `json:"server_pubkey"`
	DNS            string `json:"dns"`
}

func main() {
	server := defaultServer
	if v := os.Getenv("TOLLGATE_SERVER"); v != "" {
		server = v
	}

	var positional []string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-h", args[i] == "--help":
			usage()
		case args[i] == "--server":
			if i+1 >= len(args) {
				fatal("--server requires a value")
			}
			server = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--server="):
			server = strings.TrimPrefix(args[i], "--server=")
		default:
			positional = append(positional, args[i])
		}
	}

	if len(positional) == 0 {
		usage()
	}
	token := positional[0]
	host := hostnameOnly(server)

	privateKey, publicKey, err := loadOrGenerateKeypair()
	if err != nil {
		fatal("key setup failed: %v", err)
	}

	resp, err := connect(host, token, publicKey)
	if err != nil {
		fatal("connection failed: %v", err)
	}

	printConfig(privateKey, resp, host)
}

func loadOrGenerateKeypair() (private, public string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("find home dir: %w", err)
	}
	dir := filepath.Join(home, ".tollgate")
	privPath := filepath.Join(dir, "wg-private.key")

	if data, rErr := os.ReadFile(privPath); rErr == nil {
		private = strings.TrimSpace(string(data))
		public, err = derivePubkey(private)
		if err != nil {
			return "", "", fmt.Errorf("derive pubkey from existing key: %w", err)
		}
		return private, public, nil
	}

	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("create %s: %w", dir, err)
	}

	private, public, err = generateKeypair()
	if err != nil {
		return "", "", err
	}

	if err = os.WriteFile(privPath, []byte(private+"\n"), 0o600); err != nil {
		return "", "", fmt.Errorf("write private key: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Generated new WireGuard keypair in %s\n", dir)
	return private, public, nil
}

func generateKeypair() (private, public string, err error) {
	priv, err := exec.Command("wg", "genkey").Output()
	if err != nil {
		return "", "", fmt.Errorf("wg genkey: %w (is wireguard-tools installed?)", err)
	}
	private = strings.TrimSpace(string(priv))

	public, err = derivePubkey(private)
	if err != nil {
		return "", "", err
	}
	return private, public, nil
}

func derivePubkey(private string) (string, error) {
	cmd := exec.Command("wg", "pubkey")
	cmd.Stdin = strings.NewReader(private + "\n")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("wg pubkey: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func connect(host, token, pubkey string) (*connectResponse, error) {
	body, err := json.Marshal(connectRequest{Token: token, Pubkey: pubkey})
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	url := fmt.Sprintf("http://%s:%s/v1/wg/connect", host, apiPort)

	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var cr connectResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if cr.ClientIP == "" || cr.ServerPubkey == "" {
		return nil, fmt.Errorf("incomplete server response: %+v", cr)
	}

	return &cr, nil
}

func printConfig(privateKey string, resp *connectResponse, host string) {
	dns := resp.DNS
	if dns == "" {
		dns = "1.1.1.1"
	}

	fmt.Printf("[Interface]\n")
	fmt.Printf("PrivateKey = %s\n", privateKey)
	fmt.Printf("Address = %s/32\n", resp.ClientIP)
	fmt.Printf("DNS = %s\n", dns)
	fmt.Printf("\n")
	fmt.Printf("[Peer]\n")
	fmt.Printf("PublicKey = %s\n", resp.ServerPubkey)
	fmt.Printf("Endpoint = %s:%s\n", host, wgPort)
	fmt.Printf("AllowedIPs = 0.0.0.0/0\n")

	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "Session: %d seconds (%d min)\n", resp.SessionTimeout, resp.SessionTimeout/60)
	fmt.Fprintf(os.Stderr, "Client IP: %s\n", resp.ClientIP)
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "Save this config and run: wg-quick up wg-tollgate\n")
	fmt.Fprintf(os.Stderr, "  sudo tee /etc/wireguard/wg-tollgate.conf < <(tollgate-wg <token>)\n")
	fmt.Fprintf(os.Stderr, "  sudo wg-quick up wg-tollgate\n")
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "To disconnect:\n")
	fmt.Fprintf(os.Stderr, "  sudo wg-quick down wg-tollgate\n")
}

func hostnameOnly(s string) string {
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}

func usage() {
	prog := filepath.Base(os.Args[0])
	fmt.Fprintf(os.Stderr, "Usage: %s <cashu-token> [--server <hostname>]\n", prog)
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "Connect to a tollgate WireGuard endpoint using a Cashu ecash token.\n")
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "Arguments:\n")
	fmt.Fprintf(os.Stderr, "  <cashu-token>    Cashu ecash token (cashuA... or cashuB...)\n")
	fmt.Fprintf(os.Stderr, "  --server <host>  Tollgate server hostname (default: %s, env: TOLLGATE_SERVER)\n", defaultServer)
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "The daemon API uses port %s; WireGuard endpoint uses port %s.\n", apiPort, wgPort)
	fmt.Fprintf(os.Stderr, "Keys are stored in ~/.tollgate/ (mode 0600).\n")
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "Output is a wg-quick config. Save it and run:\n")
	fmt.Fprintf(os.Stderr, "  sudo wg-quick up wg-tollgate\n")
	os.Exit(1)
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	os.Exit(1)
}
