package sshx

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"tern/internal/model"
)

// Target is where an SSH connection goes and whom it must find there.
type Target struct {
	Host    string
	Port    int
	User    string
	HostKey string // the confirmed host key, authorized_keys form
}

const checkTimeout = 15 * time.Second

// HostKeyAlgos is what a client may accept for a pinned key of keyType: the
// SHA-2 signature schemes for RSA, the type itself for everything else.
func HostKeyAlgos(keyType string) []string {
	if keyType == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	}
	return []string{keyType}
}

// Check is the preflight for an SSH connection: verify the pinned host key,
// authenticate with signer, and ask for the sftp subsystem. No data moves.
// The result is nil or a *model.KindError.
func Check(ctx context.Context, t Target, signer ssh.Signer) error {
	pinned, err := ParseAuthorized(t.HostKey)
	if err != nil {
		return &model.KindError{Kind: model.KindConfig, Msg: "the stored host key is not a valid public key", Err: err}
	}
	conn, addr, stop, err := dial(ctx, t.Host, t.Port, checkTimeout)
	if err != nil {
		return networkErr(addr, err)
	}
	defer conn.Close()
	defer stop()

	var mismatch bool
	cfg := &ssh.ClientConfig{
		User:              t.User,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyAlgorithms: HostKeyAlgos(pinned.Type()),
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if !bytes.Equal(key.Marshal(), pinned.Marshal()) {
				mismatch = true
				return errors.New("host key differs from the confirmed one")
			}
			return nil
		},
	}
	hostKeyErr := func(err error) error {
		return &model.KindError{Kind: model.KindHostKey,
			Msg: "the host key of " + addr + " is not the one that was confirmed; refusing to connect", Err: err}
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		switch msg := err.Error(); {
		case mismatch:
			return hostKeyErr(err)
		case strings.Contains(msg, "no common algorithm for host key"):
			// The host no longer has a key of the confirmed type at all.
			return hostKeyErr(err)
		case strings.Contains(msg, "unable to authenticate"):
			return &model.KindError{Kind: model.KindAuth,
				Msg: addr + " refused the key for user " + t.User + "; is the public key in authorized_keys?", Err: err}
		}
		return networkErr(addr, err)
	}
	client := ssh.NewClient(c, chans, reqs)
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		var open *ssh.OpenChannelError
		if errors.As(err, &open) {
			return &model.KindError{Kind: model.KindConfig, Msg: "remote refuses to open a session", Err: err}
		}
		return networkErr(addr, err)
	}
	defer sess.Close()
	if err := sess.RequestSubsystem("sftp"); err != nil {
		// x/crypto reports a refused request as a plain error; I/O failures
		// surface as net or EOF errors instead.
		var ne net.Error
		if errors.As(err, &ne) || strings.Contains(err.Error(), "EOF") {
			return networkErr(addr, err)
		}
		return &model.KindError{Kind: model.KindConfig, Msg: "remote has no sftp subsystem", Err: err}
	}
	return nil
}
