// Package id provides unique identifier helpers used across Kurwall.
package id

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/google/uuid"
)

// New returns a random UUIDv4 string. It panics only if the operating system
// entropy source is broken, which is not recoverable.
func New() string {
	return uuid.NewString()
}

// NewV7 returns a time-ordered UUID. Ordering keeps ClickHouse inserts and
// SQLite indexes friendly. It falls back to v4 on clock failure.
func NewV7() string {
	u, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return u.String()
}

// Short returns a compact random hex identifier for peer/session labels.
func Short(n int) string {
	buf := make([]byte, (n+1)/2)
	if _, err := rand.Read(buf); err != nil {
		return New()
	}
	return hex.EncodeToString(buf)[:n]
}
