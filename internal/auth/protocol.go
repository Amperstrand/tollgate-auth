package auth

// AuthRequest is the JSON payload sent by the shim (or HTTP client) to the daemon.
type AuthRequest struct {
	Username          string `json:"username"`
	MAC               string `json:"mac"`
	Password          string `json:"password,omitempty"`
	CleartextPassword string `json:"cleartext_password,omitempty"`
	ClientIP          string `json:"client_ip,omitempty"`
	NASID             string `json:"nas_id,omitempty"`
}

// AuthResponse is the JSON payload returned by the daemon.
type AuthResponse struct {
	Accept         bool   `json:"accept"`
	ReplyMessage   string `json:"reply_message"`
	SessionTimeout int    `json:"session_timeout,omitempty"`
	AcctInterval   int    `json:"acct_interval,omitempty"`
	Class          string `json:"class,omitempty"`
}

// AuthResponseFromResult converts an AuthResult to an AuthResponse.
func AuthResponseFromResult(r AuthResult) AuthResponse {
	return AuthResponse{
		Accept:         r.Accept,
		ReplyMessage:   r.ReplyMessage,
		SessionTimeout: r.SessionTimeout,
		AcctInterval:   r.AcctInterval,
		Class:          r.Class,
	}
}
