package main

import (
	"fmt"
	"log"
	"os"

	"tollgate-auth/internal/auth"
)

// Type aliases — all shared types now live in internal/auth.
// These aliases let the old handler functions and tests compile unchanged.
type (
	AuthResult    = auth.AuthResult
	Dependencies  = auth.Dependencies
	SessionStore  = auth.SessionStore
	SessionRecord = auth.SessionRecord
)

// emitResult converts an AuthResult into FreeRADIUS exec output + exit code.
// Used by the exec binary path. The daemon does NOT use this — it returns JSON.
func emitResult(r auth.AuthResult) {
	if r.LogMessage != "" {
		log.Print(r.LogMessage)
	}
	if r.Accept {
		replyMessage("%s", r.ReplyMessage)
		radiusAttr("Session-Timeout", r.SessionTimeout)
		radiusAttr("Acct-Interim-Interval", r.AcctInterval)
		if r.Class != "" {
			fmt.Printf("Class = \"%s\"\n", r.Class)
		}
		os.Exit(0)
	}
	replyMessage("%s", r.ReplyMessage)
	os.Exit(1)
}
