package sshx

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"tern/internal/rundir"
	"tern/internal/sshx/sshtest"
)

// shortRunDir is a run directory with a path short enough for a unix socket.
func shortRunDir(t *testing.T) *rundir.Dir {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "t")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	d, err := rundir.New(base, "r")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func startAgent(t *testing.T, p Parsed) (*Agent, *rundir.Dir) {
	t.Helper()
	dir := shortRunDir(t)
	a, err := StartAgent(dir, p.Raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a, dir
}

func agentClient(t *testing.T, a *Agent) agent.ExtendedAgent {
	t.Helper()
	conn, err := net.Dial("unix", a.Socket())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return agent.NewClient(conn)
}

func TestAgentAuthenticates(t *testing.T) {
	for name, p := range map[string]Parsed{"ed25519": genParsed(t), "rsa": rsaParsed(t)} {
		t.Run(name, func(t *testing.T) {
			a, _ := startAgent(t, p)
			srv := sshtest.Start(t, sshtest.WithAuthorized(p.Signer.PublicKey()))
			client, err := ssh.Dial("tcp", net.JoinHostPort(srv.Host(), strconv.Itoa(srv.Port())), &ssh.ClientConfig{
				User:            "backup",
				Auth:            []ssh.AuthMethod{ssh.PublicKeysCallback(agentClient(t, a).Signers)},
				HostKeyCallback: ssh.FixedHostKey(srv.HostPublicKey()),
			})
			if err != nil {
				t.Fatalf("login with the agent's key: %v", err)
			}
			client.Close()
		})
	}
}

func TestAgentHoldsOneKey(t *testing.T) {
	p := genParsed(t)
	a, _ := startAgent(t, p)
	keys, err := agentClient(t, a).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || AuthorizedKey(keys[0]) != p.Authorized {
		t.Errorf("List = %v", keys)
	}
}

// Whoever can reach the socket may sign with the key, nothing more: no adding
// or removing keys and no locking the agent under a running job.
func TestAgentIsReadOnly(t *testing.T) {
	p := genParsed(t)
	a, _ := startAgent(t, p)
	c := agentClient(t, a)
	other := genParsed(t)
	if err := c.Add(agent.AddedKey{PrivateKey: other.Raw}); err == nil {
		t.Error("Add succeeded")
	}
	if err := c.RemoveAll(); err == nil {
		t.Error("RemoveAll succeeded")
	}
	if err := c.Remove(p.Signer.PublicKey()); err == nil {
		t.Error("Remove succeeded")
	}
	if err := c.Lock([]byte("x")); err == nil {
		t.Error("Lock succeeded")
	}
	if keys, err := c.List(); err != nil || len(keys) != 1 {
		t.Errorf("List after refused changes = %v, %v", keys, err)
	}
}

func TestAgentSocketMode(t *testing.T) {
	a, dir := startAgent(t, genParsed(t))
	if a.Socket() != filepath.Join(dir.Path, "agent.sock") {
		t.Errorf("Socket = %q", a.Socket())
	}
	fi, err := os.Stat(a.Socket())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Errorf("mode = %v", fi.Mode())
	}
}

func TestAgentClose(t *testing.T) {
	a, _ := startAgent(t, genParsed(t))
	held, err := net.Dial("unix", a.Socket()) // a client that never hangs up
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Socket()); !os.IsNotExist(err) {
		t.Errorf("socket still exists: %v", err)
	}
	if c, err := net.Dial("unix", a.Socket()); err == nil {
		c.Close()
		t.Error("dial after Close succeeded")
	}
	if err := a.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestAgentSocketPathTooLong(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "t")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	d, err := rundir.New(base, "r-"+strings.Repeat("x", 120))
	if err != nil {
		t.Fatal(err)
	}
	if a, err := StartAgent(d, genParsed(t).Raw); err == nil {
		a.Close()
		t.Error("StartAgent accepted a socket path beyond the unix limit")
	}
}
