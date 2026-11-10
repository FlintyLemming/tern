package sshx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"tern/internal/model"
)

// HostKey is a remote host's public key as shown to the user for confirmation.
type HostKey struct {
	Type        string // e.g. "ssh-ed25519"
	Authorized  string // "<type> <base64>"; what gets pinned
	Fingerprint string // "SHA256:…"
}

const probeTimeout = 10 * time.Second

// Host key algorithms offered when probing, most preferred first.
var probeAlgos = []string{
	ssh.KeyAlgoED25519,
	ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
	ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256,
}

var errProbeDone = errors.New("sshx: host key captured")

// ParseAuthorized parses exactly one "<type> <base64>" public key line.
func ParseAuthorized(s string) (ssh.PublicKey, error) {
	key, _, opts, rest, err := ssh.ParseAuthorizedKey([]byte(s))
	if err != nil {
		return nil, err
	}
	if len(opts) > 0 || strings.TrimSpace(string(rest)) != "" {
		return nil, errors.New("sshx: expected a single public key")
	}
	return key, nil
}

// dial opens a TCP connection whose every read and write fails once timeout
// has passed or ctx ends. stop releases the ctx watcher.
func dial(ctx context.Context, host string, port int, timeout time.Duration) (conn net.Conn, addr string, stop func() bool, err error) {
	addr = net.JoinHostPort(host, strconv.Itoa(port))
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	dctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	conn, err = (&net.Dialer{}).DialContext(dctx, "tcp", addr)
	if err != nil {
		return nil, addr, nil, err
	}
	conn.SetDeadline(deadline)
	stop = context.AfterFunc(ctx, func() { conn.Close() })
	return conn, addr, stop, nil
}

func networkErr(addr string, err error) error {
	return &model.KindError{Kind: model.KindNetwork, Msg: "cannot reach " + addr, Err: err}
}

// ProbeHostKey connects just far enough to learn the host's public key, then
// hangs up. Nothing is authenticated and nothing is trusted: the caller shows
// the result to a person.
func ProbeHostKey(ctx context.Context, host string, port int) (HostKey, error) {
	conn, addr, stop, err := dial(ctx, host, port, probeTimeout)
	if err != nil {
		return HostKey{}, networkErr(addr, err)
	}
	defer conn.Close()
	defer stop()

	var got ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User:              "tern-probe",
		HostKeyAlgorithms: probeAlgos,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			got = key
			return errProbeDone // abort before any authentication
		},
	}
	c, _, _, err := ssh.NewClientConn(conn, addr, cfg)
	if c != nil {
		c.Close()
	}
	if got == nil {
		if err == nil {
			err = errors.New("handshake finished without a host key")
		}
		if strings.Contains(err.Error(), "no common algorithm") {
			return HostKey{}, &model.KindError{Kind: model.KindConfig,
				Msg: fmt.Sprintf("%s offers no supported host key type (ed25519, ecdsa, rsa-sha2)", addr), Err: err}
		}
		return HostKey{}, networkErr(addr, err)
	}
	return HostKey{Type: got.Type(), Authorized: AuthorizedKey(got), Fingerprint: ssh.FingerprintSHA256(got)}, nil
}
