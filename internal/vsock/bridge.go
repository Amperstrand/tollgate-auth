// Package vsock provides host-side connection to Firecracker microVMs
// via the vsock device.
//
// Firecracker exposes vsock as a Unix domain socket on the host. To
// connect to a guest-side listener, the host:
//  1. Dials the Unix socket (e.g. /var/lib/vms/<id>/v.sock)
//  2. Sends "CONNECT <port>\n"
//  3. Reads the "OK <host_port>\n" response
//  4. The connection is now a bidirectional stream to the guest
//
// See: https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md
package vsock

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// DefaultVsockPort is the port the tollgate-vm-agent listens on inside the VM.
const DefaultVsockPort = 52

// Dialer connects to a guest vsock listener through Firecracker's
// Unix domain socket proxy.
type Dialer struct {
	// Timeout is the maximum time for the full handshake.
	// Default: 10 seconds.
	Timeout time.Duration
}

// Dial connects to the guest vsock port through Firecracker's host-side
// Unix socket. Returns a net.Conn that bridges bidirectionally to the
// guest's vsock listener.
//
// udsPath is the path to the Firecracker vsock Unix domain socket
// (configured when creating the VM via the Firecracker API).
// port is the guest-side vsock port to connect to.
func (d *Dialer) Dial(udsPath string, port uint32) (net.Conn, error) {
	timeout := d.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	conn, err := net.DialTimeout("unix", udsPath, timeout)
	if err != nil {
		return nil, fmt.Errorf("vsock: dial unix %s: %w", udsPath, err)
	}

	deadline := time.Now().Add(timeout)
	conn.SetDeadline(deadline)

	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		conn.Close()
		return nil, fmt.Errorf("vsock: CONNECT write: %w", err)
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("vsock: handshake read: %w", err)
	}

	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "OK ") {
		conn.Close()
		return nil, fmt.Errorf("vsock: handshake failed: %q", line)
	}

	conn.SetDeadline(time.Time{})
	return conn, nil
}

// Bridge copies data bidirectionally between two connections.
// It blocks until one side closes or errors. Both connections are
// closed when the bridge returns.
//
// This is the core loop that ties an SSH session to a VM shell:
//
//	sshConn (user) ←→ vsockConn (VM agent)
func Bridge(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(b, a)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(a, b)
		done <- struct{}{}
	}()
	<-done
	a.Close()
	b.Close()
	<-done
}
