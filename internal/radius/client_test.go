package radius

import (
	"context"
	"net"
	"testing"
	"time"

	"layeh.com/radius"
	"layeh.com/radius/rfc2865"
	"layeh.com/radius/rfc2866"
)

const testSecret = "testsecret"

// startTestServer starts a RADIUS server that responds to CoA and Disconnect
// requests. Returns the server address and a shutdown function.
func startTestServer(t *testing.T, handler radius.HandlerFunc) (string, func()) {
	t.Helper()

	// Pick a random available port
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	server := &radius.PacketServer{
		Handler:      handler,
		SecretSource: radius.StaticSecretSource([]byte(testSecret)),
	}

	// Serve in background; ignore the returned error (Shutdown causes an error)
	go server.Serve(conn)

	// Give the server a moment to start
	time.Sleep(50 * time.Millisecond)

	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		server.Shutdown(ctx)
		conn.Close()
	}

	return conn.LocalAddr().String(), shutdown
}

func TestCoA_Success(t *testing.T) {
	// Use a buffered channel to avoid data race between server goroutine
	// (writes packet) and test goroutine (reads assertions).
	packetCh := make(chan *radius.Packet, 1)

	addr, shutdown := startTestServer(t, func(w radius.ResponseWriter, r *radius.Request) {
		select {
		case packetCh <- r.Packet:
		default:
		}

		switch r.Code {
		case radius.CodeCoARequest:
			w.Write(r.Response(radius.CodeCoAACK))
		default:
			w.Write(r.Response(radius.CodeCoANAK))
		}
	})
	defer shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := SendCoA(ctx, addr, testSecret, 3600, "session-123", "user@example.com")
	if err != nil {
		t.Fatalf("SendCoA returned error: %v", err)
	}

	// Verify received packet attributes
	var receivedPacket *radius.Packet
	select {
	case receivedPacket = <-packetCh:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive a packet")
	}
	if receivedPacket.Code != radius.CodeCoARequest {
		t.Errorf("expected CoA-Request (43), got %s", receivedPacket.Code)
	}

	timeout := rfc2865.SessionTimeout_Get(receivedPacket)
	if timeout != 3600 {
		t.Errorf("expected Session-Timeout=3600, got %d", timeout)
	}

	sid := rfc2866.AcctSessionID_GetString(receivedPacket)
	if sid != "session-123" {
		t.Errorf("expected Acct-Session-Id=session-123, got %q", sid)
	}

	user := rfc2865.UserName_GetString(receivedPacket)
	if user != "user@example.com" {
		t.Errorf("expected User-Name=user@example.com, got %q", user)
	}
}

func TestCoA_NAK(t *testing.T) {
	addr, shutdown := startTestServer(t, func(w radius.ResponseWriter, r *radius.Request) {
		if r.Code == radius.CodeCoARequest {
			w.Write(r.Response(radius.CodeCoANAK))
		}
	})
	defer shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := SendCoA(ctx, addr, testSecret, 3600, "session-123", "user")
	if err == nil {
		t.Fatal("expected error for CoA-NAK, got nil")
	}
}

func TestCoA_Timeout(t *testing.T) {
	// Server that never responds
	addr, shutdown := startTestServer(t, func(w radius.ResponseWriter, r *radius.Request) {
		// Drop the packet — no response
	})
	defer shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := SendCoA(ctx, addr, testSecret, 3600, "session-123", "user")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestDisconnect_Success(t *testing.T) {
	// Use a buffered channel to avoid data race (see TestCoA_Success).
	packetCh := make(chan *radius.Packet, 1)

	addr, shutdown := startTestServer(t, func(w radius.ResponseWriter, r *radius.Request) {
		select {
		case packetCh <- r.Packet:
		default:
		}

		switch r.Code {
		case radius.CodeDisconnectRequest:
			w.Write(r.Response(radius.CodeDisconnectACK))
		default:
			w.Write(r.Response(radius.CodeDisconnectNAK))
		}
	})
	defer shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := SendDisconnect(ctx, addr, testSecret, "session-456", "user@example.com")
	if err != nil {
		t.Fatalf("SendDisconnect returned error: %v", err)
	}

	// Verify received packet attributes
	var receivedPacket *radius.Packet
	select {
	case receivedPacket = <-packetCh:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive a packet")
	}
	if receivedPacket.Code != radius.CodeDisconnectRequest {
		t.Errorf("expected Disconnect-Request (40), got %s", receivedPacket.Code)
	}

	sid := rfc2866.AcctSessionID_GetString(receivedPacket)
	if sid != "session-456" {
		t.Errorf("expected Acct-Session-Id=session-456, got %q", sid)
	}

	user := rfc2865.UserName_GetString(receivedPacket)
	if user != "user@example.com" {
		t.Errorf("expected User-Name=user@example.com, got %q", user)
	}
}

func TestDisconnect_NAK(t *testing.T) {
	addr, shutdown := startTestServer(t, func(w radius.ResponseWriter, r *radius.Request) {
		if r.Code == radius.CodeDisconnectRequest {
			w.Write(r.Response(radius.CodeDisconnectNAK))
		}
	})
	defer shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := SendDisconnect(ctx, addr, testSecret, "session-456", "user")
	if err == nil {
		t.Fatal("expected error for Disconnect-NAK, got nil")
	}
}

func TestDisconnect_Timeout(t *testing.T) {
	// Server that never responds
	addr, shutdown := startTestServer(t, func(w radius.ResponseWriter, r *radius.Request) {
		// Drop the packet — no response
	})
	defer shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := SendDisconnect(ctx, addr, testSecret, "session-456", "user")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestSendCoA_EmptyAddress(t *testing.T) {
	err := SendCoA(context.Background(), "", "secret", 3600, "sid", "user")
	if err == nil {
		t.Fatal("expected error for empty nasAddr, got nil")
	}
}

func TestSendDisconnect_EmptyAddress(t *testing.T) {
	err := SendDisconnect(context.Background(), "", "secret", "sid", "user")
	if err == nil {
		t.Fatal("expected error for empty nasAddr, got nil")
	}
}

func TestSendCoA_EmptySecret(t *testing.T) {
	err := SendCoA(context.Background(), "127.0.0.1:3799", "", 3600, "sid", "user")
	if err == nil {
		t.Fatal("expected error for empty secret, got nil")
	}
}

func TestSendDisconnect_EmptySecret(t *testing.T) {
	err := SendDisconnect(context.Background(), "127.0.0.1:3799", "", "sid", "user")
	if err == nil {
		t.Fatal("expected error for empty secret, got nil")
	}
}

func TestSendCoA_OptionalFields(t *testing.T) {
	// Use a buffered channel to avoid data race (see TestCoA_Success).
	packetCh := make(chan *radius.Packet, 1)

	addr, shutdown := startTestServer(t, func(w radius.ResponseWriter, r *radius.Request) {
		select {
		case packetCh <- r.Packet:
		default:
		}
		if r.Code == radius.CodeCoARequest {
			w.Write(r.Response(radius.CodeCoAACK))
		}
	})
	defer shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Empty acctSessionID and userName — should still work
	err := SendCoA(ctx, addr, testSecret, 120, "", "")
	if err != nil {
		t.Fatalf("SendCoA with empty optional fields returned error: %v", err)
	}

	var receivedPacket *radius.Packet
	select {
	case receivedPacket = <-packetCh:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive a packet")
	}

	// Session-Timeout should be set
	timeout := rfc2865.SessionTimeout_Get(receivedPacket)
	if timeout != 120 {
		t.Errorf("expected Session-Timeout=120, got %d", timeout)
	}

	// Acct-Session-Id should not be present
	sid := rfc2866.AcctSessionID_GetString(receivedPacket)
	if sid != "" {
		t.Errorf("expected no Acct-Session-Id, got %q", sid)
	}

	// User-Name should not be present
	user := rfc2865.UserName_GetString(receivedPacket)
	if user != "" {
		t.Errorf("expected no User-Name, got %q", user)
	}
}
