package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"tollgate-auth/internal/cashu"
	"tollgate-auth/internal/config"
	"tollgate-auth/internal/fakeverity"
	"tollgate-auth/internal/firecracker"
	"tollgate-auth/internal/sessiond"
	"tollgate-auth/internal/vsock"

	"github.com/creack/pty"
	"github.com/gliderlabs/ssh"
	cryptossh "golang.org/x/crypto/ssh"
)

// --- Configuration ---

const (
	// Port 2222: non-privileged port so the server can start as root
	// (needed for chroot + useradd), then drop to nobody:nogroup inside
	// the jail. Port 22 stays free for admin SSH (sshd).
	Port          = 2222
	RateSecPerSat = 10
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
	vmMode         = config.GetEnv("TOLLGATE_VM_MODE", "chroot")
	vmFallback     = config.GetEnv("TOLLGATE_VM_FALLBACK", "true")
	fcDaemonURL    = config.GetEnv("TOLLGATE_FC_DAEMON", "http://127.0.0.1:8081")
	fcVsockDir     = config.GetEnv("TOLLGATE_FC_VSOCK_DIR", "/var/lib/vps-on-demand/vms")
)

// --- Jail Management ---

// safeGuestNamePattern is the strict allowlist a guest username must match.
// The value is constructed as "g-" + hashPrefix (hex), so it is safe-by-
// construction today. This check exists as defense-in-depth: if the
// construction logic ever changes, this guard rejects anything that could
// escape the SessionDir via path traversal or break argv boundaries.
//
// Allowed: lowercase ASCII letters, digits, hyphen. Length 1-64.
// Anything else is rejected (returns false). The guest name flows into
// `cp -a`, `rm -rf`, and `chroot` invocations as an argv element — though
// execve keeps argv slots separate (no shell), we still don't want a
// guest name like "../etc" reaching those commands.
var safeGuestNamePattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// validateGuestName rejects any guest name that could traverse paths,
// overflow argv, or break the chroot boundary. Returns the cleaned name
// and a nil error on success.
func validateGuestName(guest string) (string, error) {
	if !safeGuestNamePattern.MatchString(guest) {
		return "", fmt.Errorf("unsafe guest name rejected: %q (must match %s)", guest, safeGuestNamePattern.String())
	}
	// Reject consecutive or leading/trailing hyphens defensively — current
	// construction never produces them, but a future change might.
	if strings.Contains(guest, "--") || guest[0] == '-' || guest[len(guest)-1] == '-' {
		return "", fmt.Errorf("unsafe guest name (bad hyphen placement): %q", guest)
	}
	return guest, nil
}

func createJail(guest string, tokenData *cashu.TokenData, seconds int) error {
	clean, err := validateGuestName(guest)
	if err != nil {
		return err
	}
	jailPath := SessionDir + "/" + clean

	cmd := exec.Command("cp", "-r", "--preserve=mode", JailTemplate, jailPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("jail copy failed: %s: %w", string(out), err)
	}

	guestHome := filepath.Join(jailPath, "home", clean)
	if err := os.MkdirAll(guestHome, 0755); err != nil {
		return fmt.Errorf("create guest home failed: %w", err)
	}
	os.Chown(guestHome, 65534, 65534)

	return nil
}

func cleanupJail(name string) {
	// Validate before deleting — never pass attacker-influenced strings to
	// `rm -rf`. If the name is invalid (which should never happen since it
	// comes from our own state), bail and log instead of risking a bad path.
	clean, err := validateGuestName(name)
	if err != nil {
		log.Printf("cleanupJail: refusing to delete unsafe name: %v", err)
		return
	}
	log.Printf("Cleaning up jail: %s", clean)
	jailPath := SessionDir + "/" + clean
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

// handleFirecrackerSession creates a Firecracker microVM, bridges the SSH
// session to the VM's vsock agent, and destroys the VM when the session ends.
func handleFirecrackerSession(s ssh.Session, decision AuthDecision, seconds int, guest string) error {
	fcClient := firecracker.NewClient(fcDaemonURL)

	vmResp, err := fcClient.CreateVM(firecracker.VMSpec{
		CPUs:       1,
		MemMB:      256,
		DiskMB:     512,
		TTLSeconds: seconds + 60,
	})
	if err != nil {
		return fmt.Errorf("create VM: %w", err)
	}

	log.Printf("VM created: id=%s port=%d for %s", vmResp.ID, vmResp.PublicPort, guest)

	vsockPath := fcVsockDir + "/" + vmResp.ID + "/v.sock"
	time.Sleep(2 * time.Second)

	dialer := &vsock.Dialer{}
	vsockConn, err := dialer.Dial(vsockPath, vsock.DefaultVsockPort)
	if err != nil {
		fcClient.DestroyVM(vmResp.ID)
		return fmt.Errorf("vsock dial: %w", err)
	}

	log.Printf("vsock connected to VM %s for %s", vmResp.ID, guest)

	io.WriteString(s, fmt.Sprintf("\r\n  +======================================+\r\n"))
	io.WriteString(s, fmt.Sprintf("  |        CASHU TOLLGATE (microVM)      |\r\n"))
	io.WriteString(s, fmt.Sprintf("  +======================================+\r\n"))
	io.WriteString(s, fmt.Sprintf("  |  Time:   %d sec                      |\r\n", seconds))
	io.WriteString(s, fmt.Sprintf("  |  VM ID:  %s           |\r\n", vmResp.ID))
	io.WriteString(s, fmt.Sprintf("  +======================================+\r\n\r\n"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	timer := time.AfterFunc(time.Duration(seconds)*time.Second, func() {
		io.WriteString(s, "\r\n\r\n  === Time's up! VM destroying. ===\r\n")
		vsockConn.Close()
	})
	defer timer.Stop()

	go func() {
		io.Copy(vsockConn, s)
		select {
		case <-ctx.Done():
		default:
			vsockConn.Close()
		}
	}()

	io.Copy(s, vsockConn)
	cancel()

	fcClient.DestroyVM(vmResp.ID)
	log.Printf("VM destroyed: id=%s for %s", vmResp.ID, guest)

	s.Exit(0)
	return nil
}

// --- Main ---

func main() {
	os.MkdirAll(BaseDir, 0755)
	os.MkdirAll(SessionDir, 0755)

	deps := &SSHDependencies{
		Verifier:     fakeverity.NewProductionVerifier(WalletDir),
		Bootstrapper: sessiond.NewClient(sshSessiondURL),
		AuthMode:     sshAuthMode,
		SessiondURL:  sshSessiondURL,
		WalletDir:    WalletDir,
		TokensLog:    TokensLogFile,
	}

	ssh.Handle(func(s ssh.Session) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Recovered panic in session handler: %v", r)
			}
		}()

		username := s.User()
		log.Printf("Session request from %s, user=%d chars", s.RemoteAddr(), len(username))

		var decision AuthDecision
		if os.Getenv("TOLLGATE_VM_TEST") == "true" && vmMode == "firecracker" {
			decision = AuthDecision{
				Accept:    true,
				Guest:     "g-test",
				Seconds:   300,
				TokenData: &cashu.TokenData{Amount: 30, Mint: "test", Unit: "sat"},
				TokenHash: "0000000000000000",
			}
			log.Printf("TEST MODE: skipping auth, decision=accept")
		} else {
			decision = processSSHAuth(deps, username, s.RemoteAddr().String())
		}

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
		// Defense-in-depth: validate the guest name ONCE before any use.
		// Rejects path traversal, argv overflow, or chroot escape attempts.
		// Current construction ("g-" + hex hash) is always safe, but this
		// guard prevents regressions if construction changes.
		if _, err := validateGuestName(guest); err != nil {
			log.Printf("Rejecting session: unsafe guest name: %v", err)
			io.WriteString(s, "\r\nError: invalid session identifier\r\n\r\n")
			s.Exit(1)
			return
		}
		seconds := decision.Seconds
		tokenData := decision.TokenData

		// --- Firecracker microVM mode (optional) ---
		if vmMode == "firecracker" {
			if err := handleFirecrackerSession(s, decision, seconds, guest); err == nil {
				return
			} else {
				log.Printf("Firecracker failed, falling back to chroot: %v", err)
				if vmFallback == "false" {
					io.WriteString(s, "\r\nError: VM session unavailable\r\n\r\n")
					s.Exit(1)
					return
				}
			}
		}

		// --- Chroot mode (default or fallback) ---
		// Create jail
		if err := createJail(guest, tokenData, seconds); err != nil {
			log.Printf("Failed to create jail: %v", err)
			io.WriteString(s, "\r\nError: could not create session\r\n\r\n")
			s.Exit(1)
			return
		}

		jailPath := SessionDir + "/" + guest
		_, winCh, _ := s.Pty()
		cmd := exec.Command("chroot", "--userspec=nobody:nogroup", jailPath, "/bin/tollgate-shell")
		cmd.Env = []string{
			"PATH=/bin",
			"HOME=/home/" + guest,
			"TOLLGATE_AMOUNT=" + strconv.Itoa(tokenData.Amount),
			"TOLLGATE_DURATION=" + strconv.Itoa(seconds),
			"TOLLGATE_MINT=" + tokenData.Mint,
			"TOLLGATE_GUEST=" + guest,
			"TOLLGATE_SESSION_START=" + strconv.FormatInt(time.Now().Unix(), 10),
			"TERM=xterm-256color",
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

		ctx, cancel := context.WithCancel(context.Background())
		cleanupOnce := sync.Once{}
		cleanup := func() {
			cleanupOnce.Do(func() {
				cancel()
				ptmx.Close()
				cmd.Process.Kill()
				cmd.Wait()
				cleanupJail(guest)
				log.Printf("Session ended: %s", guest)
			})
		}
		defer cleanup()

		// safeWrite wraps s.Write in a recover to catch SSH library
		// internal "close of closed channel" panics during teardown.
		safeWrite := func(p []byte) (n int, err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("session write panic: %v", r)
				}
			}()
			return s.Write(p)
		}

		// safeRead wraps s.Read in a recover for the same reason.
		safeRead := func(p []byte) (n int, err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("session read panic: %v", r)
				}
			}()
			return s.Read(p)
		}

		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Recovered panic in resize goroutine: %v", r)
				}
			}()
			for win := range winCh {
				setWinsize(ptmx, win.Width, win.Height)
			}
		}()

		// Timer: kill session on timeout
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Recovered panic in timer goroutine: %v", r)
				}
			}()
			select {
			case <-time.After(time.Duration(seconds) * time.Second):
				log.Printf("Time's up: %s", guest)
				io.WriteString(s, "\r\n\r\n  === Time's up! Session ending. ===\r\n")
				cmd.Process.Signal(syscall.SIGTERM)
				time.Sleep(500 * time.Millisecond)
				cleanup()
			case <-ctx.Done():
			}
		}()

		// SSH → PTY (user input to shell)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Recovered panic in s→ptmx goroutine: %v", r)
				}
			}()
			buf := make([]byte, 1024)
			for {
				n, err := safeRead(buf)
				if n > 0 {
					if _, werr := ptmx.Write(buf[:n]); werr != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()

		// PTY → SSH (shell output to user) — blocks until shell exits
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Recovered panic in ptmx→s main loop: %v", r)
				}
			}()
			buf := make([]byte, 1024)
			for {
				n, err := ptmx.Read(buf)
				if n > 0 {
					if _, werr := safeWrite(buf[:n]); werr != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()

		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Recovered panic in s.Exit: %v", r)
				}
			}()
			s.Exit(0)
		}()
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
