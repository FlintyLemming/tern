// Package auth handles the administrator password, sessions and login rate limiting.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

const (
	MinPasswordLen = 10
	// MaxSecretLen caps every password and passphrase, in bytes. Longer input
	// is refused before any key derivation runs.
	MaxSecretLen = 1024
)

const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// kdfMu lets one derivation run at a time: each costs 64 MiB, and a burst of
// logins must not be able to exhaust a small NAS.
var kdfMu sync.Mutex

func derive(pw, salt []byte, t, m uint32, p uint8, n uint32) []byte {
	kdfMu.Lock()
	defer kdfMu.Unlock()
	return argon2.IDKey(pw, salt, t, m, p, n)
}

// HashPassword returns "$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>".
func HashPassword(pw string) (string, error) {
	if len(pw) > MaxSecretLen {
		return "", errors.New("auth: password too long")
	}
	salt := make([]byte, argonSaltLen)
	rand.Read(salt)
	hash := derive([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(hash)), nil
}

// VerifyPassword reports whether pw matches encoded, comparing in constant time.
func VerifyPassword(encoded, pw string) bool {
	if len(pw) > MaxSecretLen {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var m, t uint32
	var p uint8
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || n != 3 {
		return false
	}
	// Bounds keep a corrupted row from turning into a crash or a huge allocation.
	if t < 1 || t > 16 || p < 1 || m < 8*uint32(p) || m > 1<<20 {
		return false
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false
	}
	got := derive([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
