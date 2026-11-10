package sshx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"tern/internal/rundir"
)

// A unix socket path must fit in sockaddr_un: 104 bytes on the BSDs and
// macOS, 108 on Linux, including the terminating NUL.
const maxSocketPath = 103

// Agent is an in-process ssh-agent holding exactly one key. OpenSSH child
// processes reach it through IdentityAgent, so the private key itself never
// leaves this process and never becomes a file.
type Agent struct {
	path string
	ln   net.Listener

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

// StartAgent listens on <dir>/agent.sock (mode 0600) and serves raw, the
// private key as returned in Parsed.Raw.
func StartAgent(dir *rundir.Dir, raw any) (*Agent, error) {
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: raw}); err != nil {
		return nil, fmt.Errorf("sshx: load key into agent: %w", err)
	}
	path := filepath.Join(dir.Path, "agent.sock")
	if len(path) > maxSocketPath {
		return nil, fmt.Errorf("sshx: agent socket path %q is %d bytes, over the unix socket limit of %d; use a shorter TERN_RUNTIME_DIR",
			path, len(path), maxSocketPath)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("sshx: agent socket: %w", err)
	}
	// The mode a socket is born with depends on the umask; the run directory
	// is 0700, so nobody else could reach it in between.
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		os.Remove(path)
		return nil, fmt.Errorf("sshx: agent socket: %w", err)
	}
	a := &Agent{path: path, ln: ln, conns: map[net.Conn]struct{}{}}
	a.wg.Add(1)
	go a.accept(signOnly{keyring.(agent.ExtendedAgent)})
	return a, nil
}

func (a *Agent) accept(ag agent.Agent) {
	defer a.wg.Done()
	for {
		c, err := a.ln.Accept()
		if err != nil {
			return
		}
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			c.Close()
			return
		}
		a.conns[c] = struct{}{}
		a.wg.Add(1)
		a.mu.Unlock()
		go func() {
			defer a.wg.Done()
			agent.ServeAgent(ag, c) // returns when the client hangs up or Close cuts it
			a.mu.Lock()
			delete(a.conns, c)
			a.mu.Unlock()
			c.Close()
		}()
	}
}

// Socket is the path to hand to ssh as IdentityAgent.
func (a *Agent) Socket() string { return a.path }

// Close stops listening, cuts every client off and deletes the socket.
func (a *Agent) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	err := a.ln.Close()
	for c := range a.conns {
		c.Close()
	}
	a.mu.Unlock()
	a.wg.Wait()
	if rmErr := os.Remove(a.path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		err = errors.Join(err, rmErr)
	}
	return err
}

var errAgentReadOnly = errors.New("sshx: this agent only lists and signs")

// signOnly exposes listing and signing and refuses everything that would
// change the agent: a client is a child process, not an administrator.
type signOnly struct{ inner agent.ExtendedAgent }

func (s signOnly) List() ([]*agent.Key, error) { return s.inner.List() }
func (s signOnly) Sign(key ssh.PublicKey, data []byte) (*ssh.Signature, error) {
	return s.inner.Sign(key, data)
}
func (s signOnly) SignWithFlags(key ssh.PublicKey, data []byte, flags agent.SignatureFlags) (*ssh.Signature, error) {
	return s.inner.SignWithFlags(key, data, flags)
}
func (s signOnly) Signers() ([]ssh.Signer, error) { return s.inner.Signers() }
func (signOnly) Add(agent.AddedKey) error         { return errAgentReadOnly }
func (signOnly) Remove(ssh.PublicKey) error       { return errAgentReadOnly }
func (signOnly) RemoveAll() error                 { return errAgentReadOnly }
func (signOnly) Lock([]byte) error                { return errAgentReadOnly }
func (signOnly) Unlock([]byte) error              { return errAgentReadOnly }
func (signOnly) Extension(string, []byte) ([]byte, error) {
	return nil, agent.ErrExtensionUnsupported
}
