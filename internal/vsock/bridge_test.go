package vsock

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestDial_HandshakeSuccess(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "v.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 64)
		n, _ := conn.Read(buf)
		got := string(buf[:n])
		want := "CONNECT 52\n"
		if got != want {
			t.Errorf("handshake got %q, want %q", got, want)
		}
		conn.Write([]byte("OK 1073741824\n"))
		io.Copy(conn, conn)
	}()

	d := &Dialer{}
	conn, err := d.Dial(sockPath, 52)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	if conn == nil {
		t.Fatal("conn is nil")
	}

	conn.Write([]byte("hello"))
	resp := make([]byte, 5)
	n, err := conn.Read(resp)
	if err != nil || n != 5 || string(resp) != "hello" {
		t.Errorf("echo: n=%d resp=%q err=%v", n, resp, err)
	}
}

func TestDial_HandshakeReject(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "reject.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 64)
		conn.Read(buf)
		conn.Write([]byte("ERR connection refused\n"))
	}()

	d := &Dialer{}
	_, err = d.Dial(sockPath, 52)
	if err == nil {
		t.Fatal("expected error on rejected handshake")
	}
	ln.Close()
}

func TestDial_ConnectionRefused(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "dead.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	os.Remove(sockPath)

	d := &Dialer{}
	_, err = d.Dial(addr, 52)
	if err == nil {
		t.Fatal("expected error on closed socket")
	}
}
