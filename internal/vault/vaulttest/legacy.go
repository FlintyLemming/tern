// Package vaulttest writes vaults in the formats of earlier tern versions,
// for tests of what the current tern does with an old database.
//
// It deliberately does not use package vault: the format is spelled out
// here once more, so a change to the writer in vault cannot silently change
// what "an old database" means in the tests.
package vaulttest

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	"tern/internal/store"
)

// Passphrase writes the vault rows of a database that tern 0.1 set up in
// passphrase mode: a random data key wrapped by an Argon2id key of
// passphrase (t=3, 64 MiB, 4 threads, 16-byte salt) with XChaCha20-Poly1305
// under the associated data "tern/dek/v1". Nothing is written to any
// secrets directory. It returns the data key, so a test can seal values the
// way tern 0.1 did.
func Passphrase(ctx context.Context, st *store.Store, passphrase string) ([]byte, error) {
	salt := make([]byte, 16)
	rand.Read(salt)
	kdf, err := json.Marshal(map[string]any{
		"salt": base64.StdEncoding.EncodeToString(salt), "time": 3, "memory_kib": 64 * 1024, "threads": 4,
	})
	if err != nil {
		return nil, err
	}
	master := argon2.IDKey([]byte(passphrase), salt, 3, 64*1024, 4, 32)
	dek := make([]byte, 32)
	rand.Read(dek)
	wrapped, err := Seal(master, "tern/dek/v1", dek)
	if err != nil {
		return nil, err
	}
	for _, kv := range [][2]string{{"vault.mode", "passphrase"}, {"vault.dek", wrapped}, {"vault.kdf", string(kdf)}} {
		if err := st.SetMeta(ctx, kv[0], kv[1]); err != nil {
			return nil, fmt.Errorf("vaulttest: %w", err)
		}
	}
	return dek, nil
}

// Seal encrypts plaintext with key the way every tern version so far has:
// "v1." + base64url(24-byte nonce ‖ XChaCha20-Poly1305 ciphertext), with aad
// as associated data.
func Seal(key []byte, aad string, plaintext []byte) (string, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	rand.Read(nonce)
	return "v1." + base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, plaintext, []byte(aad))), nil
}
