// Package sshx is tern's SSH toolbox: keys, host key probing, preflight
// checks, and what OpenSSH child processes need to authenticate.
package sshx

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"

	"golang.org/x/crypto/ssh"
)

var (
	ErrPassphraseRequired = errors.New("sshx: private key is encrypted and needs its passphrase")
	ErrBadPassphrase      = errors.New("sshx: wrong passphrase for private key")
	ErrUnsupportedKey     = errors.New("sshx: not a supported private key")
)

// Parsed is a private key in the forms the rest of tern consumes.
type Parsed struct {
	Raw         any        // what ssh.ParseRawPrivateKey returned; goes to the agent
	Signer      ssh.Signer // for preflight checks
	Authorized  string     // "ssh-ed25519 AAAA…", no comment
	Fingerprint string     // "SHA256:…"
	Encrypted   bool       // the PEM was passphrase-protected
}

// Generate creates an ed25519 key as an unencrypted OpenSSH private key.
func Generate() (privPEM []byte, p Parsed, err error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, Parsed{}, err
	}
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, Parsed{}, err
	}
	privPEM = pem.EncodeToMemory(blk)
	p, err = ParsePrivate(privPEM, nil)
	return privPEM, p, err
}

// Normalize makes pasted key text parseable: CRLF and lone CR become LF,
// surrounding whitespace goes, and the result ends in exactly one newline.
func Normalize(privPEM []byte) []byte {
	b := bytes.ReplaceAll(privPEM, []byte("\r\n"), []byte("\n"))
	b = bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
	b = bytes.TrimSpace(b)
	return append(b, '\n')
}

// ParsePrivate parses a PEM private key (OpenSSH, PKCS#1, PKCS#8 or SEC1),
// normalizing it first. passphrase is used only when the key is encrypted.
func ParsePrivate(privPEM, passphrase []byte) (Parsed, error) {
	privPEM = Normalize(privPEM)
	var encrypted bool
	raw, err := ssh.ParseRawPrivateKey(privPEM)
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		if len(passphrase) == 0 {
			return Parsed{}, ErrPassphraseRequired
		}
		encrypted = true
		raw, err = ssh.ParseRawPrivateKeyWithPassphrase(privPEM, passphrase)
		if errors.Is(err, x509.IncorrectPasswordError) {
			return Parsed{}, ErrBadPassphrase
		}
	}
	if err != nil {
		return Parsed{}, fmt.Errorf("%w: %v", ErrUnsupportedKey, err)
	}
	switch raw.(type) {
	case *ed25519.PrivateKey, ed25519.PrivateKey, *ecdsa.PrivateKey, *rsa.PrivateKey:
	default:
		return Parsed{}, fmt.Errorf("%w: key type %T", ErrUnsupportedKey, raw)
	}
	signer, err := ssh.NewSignerFromKey(raw)
	if err != nil {
		return Parsed{}, fmt.Errorf("%w: %v", ErrUnsupportedKey, err)
	}
	return Parsed{
		Raw:         raw,
		Signer:      signer,
		Authorized:  AuthorizedKey(signer.PublicKey()),
		Fingerprint: ssh.FingerprintSHA256(signer.PublicKey()),
		Encrypted:   encrypted,
	}, nil
}

// AuthorizedKey renders pub as "<type> <base64>" without newline or comment.
func AuthorizedKey(pub ssh.PublicKey) string {
	return string(bytes.TrimSpace(ssh.MarshalAuthorizedKey(pub)))
}
