// Package config provides shared configuration helpers for tollgate binaries.
package config

import "os"

// GetEnv returns the value of the environment variable key, or fallback if
// unset or empty.
func GetEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
