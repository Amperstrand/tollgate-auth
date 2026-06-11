package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"tollgate-auth/internal/cashu"
	"tollgate-auth/internal/sessiond"

	"github.com/creack/pty"
	"github.com/gliderlabs/ssh"
	cryptossh "golang.org/x/crypto/ssh"
)

// --- Configuration ---

const (
	Port          = 2222
	RateSecPerSat = 60
	BaseDir       = "/opt/cashu-tollgate"
	JailTemplate  = BaseDir + "/jail-template"
	SessionDir    = BaseDir + "/sessions"
	WalletDir     = "/var/lib/cashu-wallet"
	WalletFile    = BaseDir + "/wallet.jsonl"
	TokensLogFile = BaseDir + "/tokens.log"
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

var (
	sshAuthMode    = getEnv("TOLLGATE_AUTH_MODE", "local")
	sshSessiondURL = getEnv("TOLLGATE_SESSIOND_URL", "http://127.0.0.1:2121")
)

// --- Session Metadata ---

type SessionMeta struct {
	Started  float64 `json:"started"`
	Duration int     `json:"duration"`
	Guest    string  `json:"guest"`
	Mint     string  `json:"mint"`
	Amount   int     `json:"amount"`
}

// --- Jail Management ---

func guestUsername(tokenStr string) string {
	return "g-" + cashu.TokenHash(tokenStr)[:8]
}

func createJail(guest string, tokenData *cashu.TokenData, seconds int) error {
	jailPath := SessionDir + "/" + guest

	cmd := exec.Command("cp", "-a", JailTemplate, jailPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("jail copy failed: %s: %w", string(out), err)
	}

	meta := SessionMeta{
		Started:  float64(time.Now().Unix()),
		Duration: seconds,
		Guest:    guest,
		Mint:     tokenData.Mint,
		Amount:   tokenData.Amount,
	}
	data, _ := json.Marshal(meta)
	metaPath := jailPath + "/home/nobody/.tollgate"
	if err := os.WriteFile(metaPath, data, 0600); err != nil {
		return fmt.Errorf("write metadata failed: %w", err)
	}
	os.Chown(metaPath, 65534, 65534)

	return nil
}

func cleanupJail(name string) {
	log.Printf("Cleaning up jail: %s", name)
	jailPath := SessionDir + "/" + name
	exec.Command("rm", "-rf", jailPath).Run()
}

// --- PTY Window Resize ---

func setWinsize(f *os.File, w, h int) {
	syscall.Syscall(
		syscall.SYS_IOCTL,
		f.Fd(),
		uintptr(syscall.TIOCSWINSZ),
		uintptr(unsafe.Pointer(&struct{ h, w, x, y uint16 }{
			uint16(h), uint16(w), 0, 0,
		})),
	)
}

// --- MOTD ---

func renderMOTD(tokenData *cashu.TokenData, guest string, seconds int) string {
	minutes := tokenData.Amount
	isTest := isTestMint(tokenData.Mint)
	mintDisplay := tokenData.Mint
	if len(mintDisplay) > 36 {
		mintDisplay = mintDisplay[:33] + "..."
	}
	testStr := "NO"
	if isTest {
		testStr = "YES"
	}

	return fmt.Sprintf(
		"\r\n"+
			"  +======================================+\r\n"+
			"  |        CASHU TOLLGATE                |\r\n"+
			"  +======================================+\r\n"+
			"  |  Mint:   %-28s |\r\n"+
			"  |  Amount: %4d %-23s |\r\n"+
			"  |  Time:   %4d min (%5d sec)       |\r\n"+
			"  |  User:   %-28s |\r\n"+
			"  |  Test:   %-28s |\r\n"+
			"  +======================================+\r\n"+
			"  |  Type 'timeleft' to see time left    |\r\n"+
			"  |  Session ends when time runs out.    |\r\n"+
			"  +======================================+\r\n"+
			"\r\n",
		mintDisplay, minutes, tokenData.Unit, minutes, seconds, guest, testStr,
	)
}

func isTestMint(mintURL string) bool {
	return strings.Contains(strings.ToLower(mintURL), "test")
}

// --- Main ---

func main() {
	os.MkdirAll(BaseDir, 0755)
	os.MkdirAll(SessionDir, 0755)

	replay := cashu.NewReplayGuard(BaseDir + "/spent.txt")

	ssh.Handle(func(s ssh.Session) {
		username := s.User()
		log.Printf("Session request from %s, user=%d chars", s.RemoteAddr(), len(username))

		// 1. Decode token
		tokenData, err := cashu.DecodeToken(username)
		if err != nil {
			io.WriteString(s, fmt.Sprintf("\r\nError: %s\r\n\r\n", err))
			s.Exit(1)
			return
		}

		thash := cashu.TokenHash(username)
		guest := guestUsername(username)

		// 2. Check replay
		if replay.IsSpent(thash) {
			io.WriteString(s, "\r\nError: token already used\r\n\r\n")
			s.Exit(1)
			return
		}

		var seconds int
		if sshAuthMode == "delegated" {
			// Delegated mode: post token to v1 server for verification/redemption
			remoteAddr := s.RemoteAddr().String()
			mac := remoteAddr
			if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
				mac = "ssh:" + host
			}

			client := sessiond.NewClient(sshSessiondURL)
			state, err := client.Bootstrap(username, mac)
			if err != nil {
				log.Printf("Reject: delegated bootstrap failed: %v", err)
				io.WriteString(s, fmt.Sprintf("\r\nError: delegated session failed — %v\r\n\r\n", err))
				s.Exit(1)
				return
			}

			seconds = int(state.AllotmentMs / 1000)
			if seconds <= 0 {
				io.WriteString(s, "\r\nError: zero allotment from server\r\n\r\n")
				s.Exit(1)
				return
			}
			log.Printf("Delegated accept: guest=%s duration=%ds allotment=%dms", guest, seconds, state.AllotmentMs)

			// Mark as spent for local replay protection
			replay.MarkSpent(thash)
			cashu.LogToken(username, tokenData, guest, true, TokensLogFile)
		} else {
			// Local mode: verify and redeem token directly
			seconds = tokenData.Amount * RateSecPerSat

			// 3. Verify with mint
			ok, msg := cashu.VerifyWithMint(tokenData)
			if !ok {
				cashu.LogToken(username, tokenData, guest, false, TokensLogFile)
				io.WriteString(s, fmt.Sprintf("\r\nError: %s\r\n\r\n", msg))
				s.Exit(1)
				return
			}

			// 4. Redeem token to wallet
			io.WriteString(s, "  Redeeming token...\r\n")
			if err := cashu.RedeemToken(username, WalletDir); err != nil {
				log.Printf("Token redemption failed: %v", err)
				cashu.LogToken(username, tokenData, guest, false, TokensLogFile)
				io.WriteString(s, "\r\nError: token redemption failed\r\n\r\n")
				s.Exit(1)
				return
			}
			log.Printf("Token redeemed to wallet: %d sat from %s", tokenData.Amount, tokenData.Mint)

			// 5. Mark spent & log
			replay.MarkSpent(thash)
			cashu.LogToken(username, tokenData, guest, true, TokensLogFile)
		}

		// 6. Create jail
		if err := createJail(guest, tokenData, seconds); err != nil {
			log.Printf("Failed to create jail: %v", err)
			io.WriteString(s, "\r\nError: could not create session\r\n\r\n")
			s.Exit(1)
			return
		}

		// 7. MOTD
		io.WriteString(s, renderMOTD(tokenData, guest, seconds))

		// 8. Spawn chroot shell inside PTY
		jailPath := SessionDir + "/" + guest
		ptyReq, winCh, isPty := s.Pty()
		cmd := exec.Command("chroot", "--userspec=nobody:nogroup", jailPath, "/bin/sh", "-l")
		cmd.Env = []string{
			"HOME=/home/nobody",
			"PATH=/bin",
		}
		if isPty {
			cmd.Env = append(cmd.Env, fmt.Sprintf("TERM=%s", ptyReq.Term))
		}

		ptmx, err := pty.Start(cmd)
		if err != nil {
			log.Printf("PTY start failed: %v", err)
			cleanupJail(guest)
			io.WriteString(s, "\r\nError: could not start shell\r\n\r\n")
			s.Exit(1)
			return
		}

		log.Printf("Chroot shell PID=%d for %s", cmd.Process.Pid, guest)

		sessionStart := time.Now()
		done := make(chan struct{})
		cleanupOnce := sync.Once{}
		cleanup := func() {
			cleanupOnce.Do(func() {
				ptmx.Close()
				cmd.Process.Kill()
				cmd.Wait()
				s.Close()
				cleanupJail(guest)
				log.Printf("Session ended: %s", guest)
			})
		}
		defer cleanup()

		go func() {
			for win := range winCh {
				setWinsize(ptmx, win.Width, win.Height)
			}
		}()

		// Timer: kill session on timeout
		go func() {
			select {
			case <-time.After(time.Duration(seconds) * time.Second):
				log.Printf("Time's up: %s", guest)
				io.WriteString(s, "\r\n\r\n  === Time's up! Session ending. ===\r\n")
				cmd.Process.Signal(syscall.SIGTERM)
				time.Sleep(500 * time.Millisecond)
				cleanup()
			case <-done:
			}
		}()

		// Periodic time reminder
		go func() {
			reminderInterval := 60 * time.Second
			if seconds < 120 {
				reminderInterval = 15 * time.Second
			}
			ticker := time.NewTicker(reminderInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					elapsed := time.Since(sessionStart)
					remaining := time.Duration(seconds)*time.Second - elapsed
					if remaining <= 0 {
						return
					}
					mins := int(remaining.Minutes())
					secs := int(remaining.Seconds()) % 60
					io.WriteString(s, fmt.Sprintf("\r\n  [%dm %02ds remaining]\r\n", mins, secs))
				case <-done:
					return
				}
			}
		}()

		go func() {
			io.Copy(ptmx, s)
			close(done)
		}()

		io.Copy(s, ptmx)
		close(done)
	})

	// Load host keys
	hostKeys := []string{
		"/etc/ssh/ssh_host_ed25519_key",
		"/etc/ssh/ssh_host_rsa_key",
	}
	var signers []ssh.Signer
	for _, keyPath := range hostKeys {
		keyBytes, err := os.ReadFile(keyPath)
		if err != nil {
			log.Printf("Warning: could not read host key %s: %v", keyPath, err)
			continue
		}
		signer, err := cryptossh.ParsePrivateKey(keyBytes)
		if err != nil {
			log.Printf("Warning: could not parse host key %s: %v", keyPath, err)
			continue
		}
		signers = append(signers, signer)
	}
	if len(signers) == 0 {
		log.Fatal("No SSH host keys found!")
	}

	server := &ssh.Server{
		Addr:    fmt.Sprintf(":%d", Port),
		Handler: ssh.DefaultHandler,
	}
	for _, s := range signers {
		server.AddHostKey(s)
	}

	log.Printf("tollgate-auth-ssh listening on port %d", Port)
	log.Printf("Jail template: %s", JailTemplate)
	log.Printf("Session dir: %s", SessionDir)
	log.Printf("Wallet: %s", WalletFile)
	log.Printf("Token log: %s", TokensLogFile)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Shutting down...")
		server.Close()
	}()

	if err := server.ListenAndServe(); err != nil && err != ssh.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}
