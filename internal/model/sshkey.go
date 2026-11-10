package model

import "time"

// SSHKey is the public face of a stored key pair. The private key and its
// passphrase live sealed in the store and are never part of this type.
type SSHKey struct {
	ID            string
	Version       int
	Name          string
	PublicKey     string // authorized_keys form, no comment
	Fingerprint   string // "SHA256:…"
	HasPassphrase bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
