package main

import "testing"

func TestParseSocketAddress(t *testing.T) {
	cases := []struct {
		input       string
		wantNetwork string
		wantAddress string
	}{
		// Bare path → Unix socket (backwards compat with existing deployments)
		{"/run/tollgate/tollgate.sock", "unix", "/run/tollgate/tollgate.sock"},
		{"/tmp/foo.sock", "unix", "/tmp/foo.sock"},
		// Explicit unix:// scheme
		{"unix:///run/tollgate/tollgate.sock", "unix", "/run/tollgate/tollgate.sock"},
		// TCP — used in Docker deployments
		{"tcp://tollgate-daemon:8094", "tcp", "tollgate-daemon:8094"},
		{"tcp://127.0.0.1:8094", "tcp", "127.0.0.1:8094"},
		{"tcp://[::1]:8094", "tcp", "[::1]:8094"},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			gotNetwork, gotAddress := parseSocketAddress(c.input)
			if gotNetwork != c.wantNetwork {
				t.Errorf("network: got %q, want %q", gotNetwork, c.wantNetwork)
			}
			if gotAddress != c.wantAddress {
				t.Errorf("address: got %q, want %q", gotAddress, c.wantAddress)
			}
		})
	}
}

// TestParseSocketAddress_backwards_compat verifies the canonical existing
// config (bare /run/tollgate/tollgate.sock) still resolves to Unix socket.
// This is the production value from config/systemd/tollgate-daemon.service —
// changing the default would break every deployed daemon.
func TestParseSocketAddress_backwards_compat(t *testing.T) {
	const existingSystemdValue = "/run/tollgate/tollgate.sock"
	network, address := parseSocketAddress(existingSystemdValue)
	if network != "unix" || address != existingSystemdValue {
		t.Fatalf("bare path must resolve to unix socket unchanged: got %s://%s", network, address)
	}
}
