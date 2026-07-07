package cashu

import (
	"encoding/json"
	"log"
	"os"
	"time"
)

func LogToken(tokenStr string, tokenData *TokenData, guest string, accepted bool, logFile string) {
	LogTokenWithError(tokenStr, tokenData, guest, accepted, "", logFile)
}

func LogTokenWithError(tokenStr string, tokenData *TokenData, guest string, accepted bool, errMsg string, logFile string) {
	entry := map[string]interface{}{
		"ts":       time.Now().UTC().Format(time.RFC3339),
		"guest":    guest,
		"accepted": accepted,
		"mint":     tokenData.Mint,
		"amount":   tokenData.Amount,
		"unit":     tokenData.Unit,
		"hash":     TokenHash(tokenStr),
	}
	if errMsg != "" {
		entry["error"] = errMsg
	}
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		log.Printf("Warning: failed to open token log %s: %v", logFile, err)
		return
	}
	defer f.Close()
	line, _ := json.Marshal(entry)
	f.WriteString(string(line) + "\n")
}
