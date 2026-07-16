package main

import (
	"context"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tollgate-auth/internal/vsock"

	"github.com/coder/websocket"
)

const wsIdleTimeout = 10 * time.Minute

func handleVMShellWS(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/vms/")
	id = strings.TrimSuffix(id, "/shell")
	if id == "" || strings.Contains(id, "/") {
		writeJSON(w, 400, map[string]string{"error": "invalid VM ID"})
		return
	}

	if !checkAuth(r) {
		writeJSON(w, 401, map[string]string{"error": "Unauthorized"})
		return
	}

	vmsMutex.Lock()
	_, exists := vms[id]
	vmsMutex.Unlock()
	if !exists {
		writeJSON(w, 404, map[string]string{"error": "VM not found"})
		return
	}

	vsockPath := filepath.Join(vmBase, id, "v.sock")

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		log.Printf("[tollgate-fcd] WS accept failed for VM %s: %v", id, err)
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	dialer := vsock.Dialer{Timeout: 10 * time.Second}
	vconn, err := dialer.Dial(vsockPath, vsock.DefaultVsockPort)
	if err != nil {
		wctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		c.Write(wctx, websocket.MessageText, []byte("\r\nError: cannot connect to VM shell: "+err.Error()+"\r\n"))
		cancel()
		log.Printf("[tollgate-fcd] vsock dial failed for VM %s: %v", id, err)
		return
	}
	defer vconn.Close()

	log.Printf("[tollgate-fcd] WS shell connected: VM %s from %s", id, r.RemoteAddr)

	wctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	c.Write(wctx, websocket.MessageText, []byte("\r\n[connected to VM shell]\r\n"))
	cancel()

	var wg sync.WaitGroup
	wg.Add(2)

	// WS → vsock (user input to shell)
	go func() {
		defer wg.Done()
		for {
			ctx, cancel := context.WithTimeout(context.Background(), wsIdleTimeout)
			_, data, err := c.Read(ctx)
			cancel()
			if err != nil {
				vconn.Close()
				return
			}
			vconn.Write(data)
		}
	}()

	// vsock → WS (shell output to user)
	go func() {
		defer wg.Done()
		buf := make([]byte, 8192)
		for {
			n, err := vconn.Read(buf)
			if n > 0 {
				writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if wErr := c.Write(writeCtx, websocket.MessageText, buf[:n]); wErr != nil {
					cancel()
					return
				}
				cancel()
			}
			if err != nil {
				c.Close(websocket.StatusNormalClosure, "shell ended")
				return
			}
		}
	}()

	wg.Wait()
	log.Printf("[tollgate-fcd] WS shell ended: VM %s", id)
}
