package main

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// sendCoA sends a RADIUS CoA-Request to extend Session-Timeout on the NAS.
// Uses radclient CLI: echo "Session-Timeout=X" | radclient NAS_IP:3799 coa SECRET
func sendCoA(nasIP string, port string, sessionTimeout int, sessionID string, username string, secret string) error {
	attrs := fmt.Sprintf("Session-Timeout=%d", sessionTimeout)
	if sessionID != "" {
		attrs += fmt.Sprintf(",Acct-Session-Id=%s", sessionID)
	}
	if username != "" {
		attrs += fmt.Sprintf(",User-Name=%s", username)
	}

	cmd := exec.Command("radclient", "-x", fmt.Sprintf("%s:%s", nasIP, port), "coa", secret)
	cmd.Stdin = strings.NewReader(attrs + "\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("radclient coa failed: %w, output: %s", err, string(output))
	}
	return nil
}

// sendCoAOrDisconnect sends a CoA-Request to update Session-Timeout on the NAS.
// If CoA fails, falls back to Disconnect-Request so the user reconnects with
// the extended allotment.
func sendCoAOrDisconnect(nasIP string, sessionTimeout int, sessionID string, username string) {
	coaSecret := getEnv("TOLLGATE_COA_SECRET", "tollgate")
	coaPort := getEnv("TOLLGATE_COA_PORT", "3799")

	if nasIP == "" {
		log.Printf("CoA: no NAS IP, skipping")
		return
	}

	// Try CoA first
	err := sendCoA(nasIP, coaPort, sessionTimeout, sessionID, username, coaSecret)
	if err != nil {
		log.Printf("CoA: failed for nas=%s: %v, falling back to disconnect", nasIP, err)
		// Fallback: disconnect — user reconnects with extended session
		if disconnectErr := sendDisconnect(nasIP, coaPort, sessionID, username, coaSecret); disconnectErr != nil {
			log.Printf("CoA: disconnect fallback also failed for nas=%s: %v", nasIP, disconnectErr)
		} else {
			log.Printf("CoA: disconnect fallback sent for nas=%s session=%s", nasIP, sessionID)
		}
	} else {
		log.Printf("CoA: Session-Timeout updated to %ds for session=%s nas=%s", sessionTimeout, sessionID, nasIP)
	}
}
