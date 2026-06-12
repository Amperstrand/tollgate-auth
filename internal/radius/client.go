// Package radius provides a native Go RADIUS client for CoA (Change of Authorization)
// and Disconnect-Request messages per RFC 5176, replacing radclient subprocess calls.
package radius

import (
	"context"
	"fmt"
	"time"

	"layeh.com/radius"
	"layeh.com/radius/rfc2865"
	"layeh.com/radius/rfc2866"
)

// DefaultTimeout is the default RADIUS request timeout.
const DefaultTimeout = 5 * time.Second

// SendCoA sends a RADIUS CoA-Request (RFC 5176, code 43) to extend
// Session-Timeout on the NAS for an active session.
//
// Parameters:
//   - nasAddr: NAS address in "host:port" format (e.g. "192.168.1.1:3799")
//   - secret: RADIUS shared secret
//   - sessionTimeout: new Session-Timeout value in seconds
//   - acctSessionID: Acct-Session-Id to identify the session
//   - userName: User-Name attribute (may be empty)
func SendCoA(ctx context.Context, nasAddr, secret string, sessionTimeout int, acctSessionID, userName string) error {
	if nasAddr == "" {
		return fmt.Errorf("radius: nasAddr is required")
	}
	if secret == "" {
		return fmt.Errorf("radius: secret is required")
	}

	// Build CoA-Request packet
	packet := radius.New(radius.CodeCoARequest, []byte(secret))

	// Session-Timeout
	rfc2865.SessionTimeout_Add(packet, rfc2865.SessionTimeout(sessionTimeout))

	// Acct-Session-Id — identifies which session to modify
	if acctSessionID != "" {
		rfc2866.AcctSessionID_AddString(packet, acctSessionID)
	}

	// User-Name — optional, helps NAS identify the session
	if userName != "" {
		rfc2865.UserName_AddString(packet, userName)
	}

	// Apply default timeout if ctx has no deadline
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	response, err := radius.Exchange(ctx, packet, nasAddr)
	if err != nil {
		return fmt.Errorf("radius CoA exchange failed: %w", err)
	}

	switch response.Code {
	case radius.CodeCoAACK:
		return nil
	case radius.CodeCoANAK:
		return fmt.Errorf("radius CoA rejected (CoA-NAK)")
	default:
		return fmt.Errorf("radius CoA unexpected response code: %s", response.Code)
	}
}

// SendDisconnect sends a RADIUS Disconnect-Request (RFC 5176, code 40) to
// terminate a session on the NAS.
//
// Parameters:
//   - nasAddr: NAS address in "host:port" format (e.g. "192.168.1.1:3799")
//   - secret: RADIUS shared secret
//   - acctSessionID: Acct-Session-Id to identify the session
//   - userName: User-Name attribute (may be empty)
func SendDisconnect(ctx context.Context, nasAddr, secret string, acctSessionID, userName string) error {
	if nasAddr == "" {
		return fmt.Errorf("radius: nasAddr is required")
	}
	if secret == "" {
		return fmt.Errorf("radius: secret is required")
	}

	// Build Disconnect-Request packet
	packet := radius.New(radius.CodeDisconnectRequest, []byte(secret))

	// Acct-Session-Id — identifies which session to disconnect
	if acctSessionID != "" {
		rfc2866.AcctSessionID_AddString(packet, acctSessionID)
	}

	// User-Name — optional, helps NAS identify the session
	if userName != "" {
		rfc2865.UserName_AddString(packet, userName)
	}

	// Apply default timeout if ctx has no deadline
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	response, err := radius.Exchange(ctx, packet, nasAddr)
	if err != nil {
		return fmt.Errorf("radius disconnect exchange failed: %w", err)
	}

	switch response.Code {
	case radius.CodeDisconnectACK:
		return nil
	case radius.CodeDisconnectNAK:
		return fmt.Errorf("radius disconnect rejected (Disconnect-NAK)")
	default:
		return fmt.Errorf("radius disconnect unexpected response code: %s", response.Code)
	}
}
