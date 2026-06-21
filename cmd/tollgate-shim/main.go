// cmd/tollgate-shim is a lightweight bridge that FreeRADIUS's exec module calls
// instead of the full tollgate-auth-radius binary. It connects to the persistent
// tollgate-daemon over a Unix socket, forwards the auth request, and translates
// the JSON response into RADIUS exec output format.
//
// Usage (identical calling convention to tollgate-auth-radius):
//
//	tollgate-shim <username> <mac-address> [password] [cleartext-password]
//
// FreeRADIUS exec module config:
//
//	program = "/usr/local/bin/tollgate-shim %{User-Name} %{Calling-Station-Id} %{User-Password} %{Cleartext-Password}"
//
// Output format (parsed by FreeRADIUS exec module with output_pairs = reply):
//
//	Reply-Message = "Valid Cashu token: 8 sat = 8m access"
//	Session-Timeout = 480
//	Acct-Interim-Interval = 60
//	Class = "..."
//
// Exit code 0 = Access-Accept, 1 = Access-Reject.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"tollgate-auth/internal/auth"
	"tollgate-auth/internal/config"
)

const (
	defaultSocketPath = "/run/tollgate/tollgate.sock"
	socketTimeout     = 15 * time.Second // must be >= FreeRADIUS exec timeout
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <username> <mac-address> [password] [cleartext-password] [nas-id] [client-ip]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  Called by FreeRADIUS exec module. Forwards to tollgate-daemon.\n")
		os.Exit(1)
	}

	username := os.Args[1]
	mac := os.Args[2]
	password := ""
	if len(os.Args) >= 4 {
		password = os.Args[3]
	}
	clearTextPw := ""
	if len(os.Args) >= 5 {
		clearTextPw = os.Args[4]
	}
	nasID := ""
	if len(os.Args) >= 6 {
		nasID = os.Args[5]
	}
	clientIP := ""
	if len(os.Args) >= 7 {
		clientIP = os.Args[6]
	}

	socketPath := config.GetEnv("TOLLGATE_SOCKET", defaultSocketPath)

	if nasID == "" {
		nasID = config.GetEnv("TOLLGATE_NAS_ID", "")
	}
	if clientIP == "" {
		clientIP = config.GetEnv("TOLLGATE_CLIENT_IP", "")
	}

	req := auth.AuthRequest{
		Username:          username,
		MAC:               mac,
		Password:          password,
		CleartextPassword: clearTextPw,
		ClientIP:          clientIP,
		NASID:             nasID,
	}

	resp, err := sendRequest(socketPath, req)
	if err != nil {
		log.Printf("Shim error (socket=%s): %v", socketPath, err)
		replyMessage("Rejected: auth daemon unavailable")
		os.Exit(1)
	}

	// Translate JSON response to RADIUS exec output.
	if resp.Accept {
		replyMessage("%s", resp.ReplyMessage)
		if resp.SessionTimeout > 0 {
			radiusAttr("Session-Timeout", resp.SessionTimeout)
		}
		if resp.AcctInterval > 0 {
			radiusAttr("Acct-Interim-Interval", resp.AcctInterval)
		}
		if resp.Class != "" {
			fmt.Printf("Class = \"%s\"\n", resp.Class)
		}
		os.Exit(0)
	}

	replyMessage("%s", resp.ReplyMessage)
	os.Exit(1)
}

// sendRequest connects to the daemon, sends the request, and reads the response.
func sendRequest(socketPath string, req auth.AuthRequest) (*auth.AuthResponse, error) {
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(socketTimeout))

	// Send request as newline-delimited JSON.
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}
	if _, err := conn.Write([]byte("\n")); err != nil {
		return nil, fmt.Errorf("write newline: %w", err)
	}

	// Read response line.
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		return nil, fmt.Errorf("read: connection closed")
	}

	var resp auth.AuthResponse
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &resp, nil
}

// replyMessage outputs a Reply-Message attribute to stdout.
// Same sanitization as the main binary — no newlines, quotes, or commas.
func replyMessage(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", "")
	msg = strings.ReplaceAll(msg, `"`, `'`)
	msg = strings.ReplaceAll(msg, ",", ";")
	fmt.Printf("Reply-Message = \"%s\"\n", msg)
}

// radiusAttr outputs a RADIUS integer attribute.
func radiusAttr(name string, value int) {
	fmt.Printf("%s = %d\n", name, value)
}
