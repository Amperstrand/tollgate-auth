package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

const (
	defaultSSHAddr   = "localhost:2222"
	defaultListen    = ":8092"
	tokenReadTimeout = 10 * time.Second
	idleTimeout      = 5 * time.Minute
)

func handleWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		slog.Error("ws accept", "error", err)
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithTimeout(r.Context(), tokenReadTimeout)
	_, tokenBytes, err := c.Read(ctx)
	cancel()
	if err != nil {
		slog.Error("ws read token", "error", err)
		wctx, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
		c.Write(wctx, websocket.MessageText, []byte("\r\nError: could not read token\r\n"))
		cancel2()
		return
	}
	token := string(tokenBytes)
	if len(token) < 10 || len(token) > 4096 {
		wctx, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
		c.Write(wctx, websocket.MessageText, []byte("\r\nError: invalid token length\r\n"))
		cancel2()
		return
	}

	sshAddr := os.Getenv("TOLLGATE_SSH_ADDR")
	if sshAddr == "" {
		sshAddr = defaultSSHAddr
	}

	sshConfig := &ssh.ClientConfig{
		User:            token,
		Auth:            []ssh.AuthMethod{ssh.Password("")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", sshAddr, sshConfig)
	if err != nil {
		slog.Error("ssh dial", "error", err)
		wctx, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
		c.Write(wctx, websocket.MessageText, []byte("\r\nError: "+err.Error()+"\r\n"))
		cancel2()
		return
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		slog.Error("ssh session", "error", err)
		return
	}
	defer session.Close()

	if err := session.RequestPty("xterm-256color", 24, 80, ssh.TerminalModes{}); err != nil {
		slog.Error("ssh pty", "error", err)
		return
	}

	stdin, _ := session.StdinPipe()
	stdout, _ := session.StdoutPipe()

	if err := session.Shell(); err != nil {
		slog.Error("ssh shell", "error", err)
		return
	}

	slog.Info("webssh session started", "remote", r.RemoteAddr)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for {
			ctx, cancel := context.WithTimeout(context.Background(), idleTimeout)
			msgType, data, err := c.Read(ctx)
			cancel()
			if err != nil {
				stdin.Close()
				return
			}
			if msgType == websocket.MessageText {
				stdin.Write(data)
			}
		}
	}()

	go func() {
		defer wg.Done()
		buf := make([]byte, 8192)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				c.Write(writeCtx, websocket.MessageBinary, buf[:n])
				cancel()
			}
			if err != nil {
				if err != io.EOF {
					slog.Error("ssh stdout read", "error", err)
				}
				c.Close(websocket.StatusNormalClosure, "session ended")
				return
			}
		}
	}()

	session.Wait()
	wg.Wait()
	slog.Info("webssh session ended", "remote", r.RemoteAddr)
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	listenAddr := os.Getenv("TOLLGATE_WEBSSH_ADDR")
	if listenAddr == "" {
		listenAddr = defaultListen
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", handleWS)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!DOCTYPE html><html><head><meta http-equiv="refresh" content="0; url=https://amperstrand.github.io/tollgate-auth/webssh.html"></head><body>Redirecting to demo page...</body></html>`))
	})

	slog.Info("tollgate-webssh starting", "listen", listenAddr, "ssh", defaultSSHAddr)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		slog.Error("listen", "error", err)
		os.Exit(1)
	}
}
