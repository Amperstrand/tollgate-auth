package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"tollgate-auth/internal/cashu"
	"tollgate-auth/internal/config"
	"tollgate-auth/internal/fakeverity"
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

var (
	sshAuthMode    = config.GetEnv("TOLLGATE_AUTH_MODE", "local")
	sshSessiondURL = config.GetEnv("TOLLGATE_SESSIOND_URL", "http://127.0.0.1:2121")
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

// --- Main ---

func main() {
	os.MkdirAll(BaseDir, 0755)
	os.MkdirAll(SessionDir, 0755)

	replay := cashu.NewReplayGuard(BaseDir + "/spent.txt")

	deps := &SSHDependencies{
		Replay:       replay,
		Verifier:     fakeverity.NewProductionVerifier(WalletDir),
		Bootstrapper: sessiond.NewClient(sshSessiondURL),
		AuthMode:     sshAuthMode,
		SessiondURL:  sshSessiondURL,
		WalletDir:    WalletDir,
		TokensLog:    TokensLogFile,
	}

	ssh.Handle(func(s ssh.Session) {
		username := s.User()
		log.Printf("Session request from %s, user=%d chars", s.RemoteAddr(), len(username))

		decision := processSSHAuth(deps, username, s.RemoteAddr().String())

		if !decision.Accept {
			log.Printf("%s", decision.LogMsg)
			if decision.TokenData != nil {
				cashu.LogTokenWithError(username, decision.TokenData, decision.Guest, false, decision.Error, deps.TokensLog)
			}
			io.WriteString(s, fmt.Sprintf("\r\nError: %s\r\n\r\n", decision.Error))
			s.Exit(1)
			return
		}

		log.Printf("%s", decision.LogMsg)
		cashu.LogToken(username, decision.TokenData, decision.Guest, true, deps.TokensLog)

		guest := decision.Guest
		seconds := decision.Seconds
		tokenData := decision.TokenData

		// Create jail
		if err := createJail(guest, tokenData, seconds); err != nil {
			log.Printf("Failed to create jail: %v", err)
			io.WriteString(s, "\r\nError: could not create session\r\n\r\n")
			s.Exit(1)
			return
		}

		// MOTD
		io.WriteString(s, decision.MOTD)

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
