// Package model holds the domain types shared across tern.
package model

import (
	"crypto/rand"
	"encoding/hex"
)

// NewID returns 32 lowercase hex characters drawn from crypto/rand.
func NewID() string {
	var b [16]byte
	rand.Read(b[:]) // crypto/rand.Read never fails; it aborts the process instead
	return hex.EncodeToString(b[:])
}
