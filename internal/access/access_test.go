package access

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"tern/internal/model"
	"tern/internal/redact"
	"tern/internal/rundir"
	"tern/internal/sshx"
	"tern/internal/sshx/sshtest"
	"tern/internal/store"
	"tern/internal/vault"
)

const (
	keyPass = "pp-123456"
	davPass = "dav-s3cret-token"
)

type env struct {
	t  *testing.T
	st *store.Store
	v  *vault.Vault
	o  Opener
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	v := vault.New(st, t.TempDir())
	if _, err := v.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := v.Init(ctx); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, st: st, v: v, o: Opener{Store: st, Vault: v}}
}

// lock swaps in a vault loaded without its key file, which cannot open.
func (e *env) lock() {
	e.t.Helper()
	v := vault.New(e.st, e.t.TempDir())
	if st, err := v.Load(context.Background()); err != nil || st != vault.StateKeyfileMissing {
		e.t.Fatalf("vault state = %s, %v", st, err)
	}
	e.o.Vault = v
}

func (e *env) seal(aad string, plain []byte) string {
	e.t.Helper()
	s, err := e.v.Seal(aad, plain)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// addKey stores a fresh ed25519 key, encrypted with pass when non-empty.
func (e *env) addKey(pass string) (id string, p sshx.Parsed) {
	e.t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	var (
		blk *pem.Block
		err error
	)
	if pass == "" {
		blk, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		blk, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(pass))
	}
	if err != nil {
		e.t.Fatal(err)
	}
	privPEM := pem.EncodeToMemory(blk)
	if p, err = sshx.ParsePrivate(privPEM, []byte(pass)); err != nil {
		e.t.Fatal(err)
	}
	id = model.NewID()
	var passSealed string
	if pass != "" {
		passSealed = e.seal("ssh_keys/"+id+"/passphrase", []byte(pass))
	}
	now := time.Now().UTC().Truncate(time.Second)
	k := model.SSHKey{ID: id, Version: 1, Name: "key-" + id, PublicKey: p.Authorized, Fingerprint: p.Fingerprint, CreatedAt: now, UpdatedAt: now}
	if err := e.st.CreateKey(context.Background(), k, e.seal("ssh_keys/"+id+"/private", privPEM), passSealed); err != nil {
		e.t.Fatal(err)
	}
	return id, p
}

func (e *env) addSSHConn(keyID string, srv *sshtest.Server) string {
	e.t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	c := model.Connection{ID: model.NewID(), Version: 1, Type: model.ConnSSH, Name: "c-" + model.NewID(), Host: srv.Host(), Port: srv.Port(),
		Username: "backup", SSHKeyID: keyID, HostKey: sshx.AuthorizedKey(srv.HostPublicKey()), CreatedAt: now, UpdatedAt: now}
	if err := e.st.CreateConnection(context.Background(), c, ""); err != nil {
		e.t.Fatal(err)
	}
	return c.ID
}

func (e *env) addDAVConn(url, pass string) string {
	e.t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	c := model.Connection{ID: model.NewID(), Version: 1, Type: model.ConnWebDAV, Name: "dav", URL: url, Username: "me", CreatedAt: now, UpdatedAt: now}
	var sealed string
	if pass != "" {
		sealed = e.seal("connections/"+c.ID+"/password", []byte(pass))
	}
	if err := e.st.CreateConnection(context.Background(), c, sealed); err != nil {
		e.t.Fatal(err)
	}
	return c.ID
}

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

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func masked(t *testing.T, red *redact.Redactor, s string) string {
	t.Helper()
	var out bytes.Buffer
	w := red.Writer(&out)
	io.WriteString(w, s+"\n")
	w.Close()
	return out.String()
}

// loginViaAgent proves the agent behind a.SSHConfig's socket can log in to srv.
func loginViaAgent(t *testing.T, dir *rundir.Dir, srv *sshtest.Server) error {
	t.Helper()
	conn, err := net.Dial("unix", filepath.Join(dir.Path, "agent.sock"))
	if err != nil {
		return err
	}
	defer conn.Close()
	client, err := ssh.Dial("tcp", net.JoinHostPort(srv.Host(), strconv.Itoa(srv.Port())), &ssh.ClientConfig{
		User:            "backup",
		Auth:            []ssh.AuthMethod{ssh.PublicKeysCallback(agent.NewClient(conn).Signers)},
		HostKeyCallback: ssh.FixedHostKey(srv.HostPublicKey()),
	})
	if err != nil {
		return err
	}
	return client.Close()
}

func TestOpenSSH(t *testing.T) {
	e := newEnv(t)
	keyID, p := e.addKey("")
	srv := sshtest.Start(t, sshtest.WithAuthorized(p.Signer.PublicKey()))
	connID := e.addSSHConn(keyID, srv)
	dir := shortRunDir(t)

	a, closeFn, err := e.o.Open(context.Background(), connID, dir, redact.New())
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if a.Kind != "ssh" || a.SSHConfig != filepath.Join(dir.Path, "ssh_config") {
		t.Errorf("Access = %+v", a)
	}
	if want := (sshx.Target{Host: srv.Host(), Port: srv.Port(), User: "backup", HostKey: sshx.AuthorizedKey(srv.HostPublicKey())}); a.Target != want {
		t.Errorf("Target = %+v", a.Target)
	}
	if a.URL != "" || a.User != "" || a.Pass != "" {
		t.Errorf("webdav fields set on an ssh access: %+v", a)
	}
	if got := names(t, dir.Path); strings.Join(got, ",") != "agent.sock,known_hosts,ssh_config" {
		t.Errorf("run directory holds %v", got)
	}
	for _, n := range []string{"known_hosts", "ssh_config"} {
		b, err := os.ReadFile(filepath.Join(dir.Path, n))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte("PRIVATE KEY")) {
			t.Errorf("%s contains private key material", n)
		}
	}
	if !strings.Contains(string(mustRead(t, a.SSHConfig)), "IdentityAgent "+filepath.Join(dir.Path, "agent.sock")+"\n") {
		t.Error("ssh_config does not point at the agent socket")
	}
	if err := loginViaAgent(t, dir, srv); err != nil {
		t.Errorf("login through the agent: %v", err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOpenSSHWithPassphraseKey(t *testing.T) {
	e := newEnv(t)
	keyID, p := e.addKey(keyPass)
	srv := sshtest.Start(t, sshtest.WithAuthorized(p.Signer.PublicKey()))
	dir, red := shortRunDir(t), redact.New()
	_, closeFn, err := e.o.Open(context.Background(), e.addSSHConn(keyID, srv), dir, red)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if err := loginViaAgent(t, dir, srv); err != nil {
		t.Errorf("login through the agent: %v", err)
	}
	if got := masked(t, red, "Enter passphrase "+keyPass+" ok"); got != "Enter passphrase *** ok\n" {
		t.Errorf("passphrase not masked: %q", got)
	}
}

func TestOpenWebDAV(t *testing.T) {
	e := newEnv(t)
	connID := e.addDAVConn("https://dav.example/remote.php/dav", davPass)
	dir, red := shortRunDir(t), redact.New()
	a, closeFn, err := e.o.Open(context.Background(), connID, dir, red)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if a.Kind != "webdav" || a.URL != "https://dav.example/remote.php/dav" || a.User != "me" || a.Pass != davPass || a.SSHConfig != "" {
		t.Errorf("Access = %+v", a)
	}
	if got := masked(t, red, "auth "+davPass+" sent"); got != "auth *** sent\n" {
		t.Errorf("password not masked: %q", got)
	}
	if got := names(t, dir.Path); len(got) != 0 {
		t.Errorf("run directory holds %v", got)
	}
}

func TestOpenCloseStopsAgent(t *testing.T) {
	e := newEnv(t)
	keyID, p := e.addKey("")
	srv := sshtest.Start(t, sshtest.WithAuthorized(p.Signer.PublicKey()))
	dir := shortRunDir(t)
	_, closeFn, err := e.o.Open(context.Background(), e.addSSHConn(keyID, srv), dir, redact.New())
	if err != nil {
		t.Fatal(err)
	}
	closeFn()
	if _, err := os.Stat(filepath.Join(dir.Path, "agent.sock")); !os.IsNotExist(err) {
		t.Errorf("agent.sock after close: %v", err)
	}
	closeFn() // harmless twice
}

func TestOpenLocked(t *testing.T) {
	e := newEnv(t)
	keyID, p := e.addKey("")
	srv := sshtest.Start(t, sshtest.WithAuthorized(p.Signer.PublicKey()))
	sshID := e.addSSHConn(keyID, srv)
	davID := e.addDAVConn("https://dav.example/", davPass)
	e.lock()
	for _, id := range []string{sshID, davID} {
		dir := shortRunDir(t)
		if _, _, err := e.o.Open(context.Background(), id, dir, redact.New()); !errors.Is(err, vault.ErrLocked) {
			t.Errorf("Open err = %v, want vault.ErrLocked", err)
		}
		if got := names(t, dir.Path); len(got) != 0 {
			t.Errorf("run directory holds %v after a refused Open", got)
		}
		if err := e.o.Check(context.Background(), id); !errors.Is(err, vault.ErrLocked) {
			t.Errorf("Check err = %v, want vault.ErrLocked", err)
		}
	}
}

func TestOpenUnknownConnection(t *testing.T) {
	e := newEnv(t)
	if _, _, err := e.o.Open(context.Background(), "nope", shortRunDir(t), redact.New()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want store.ErrNotFound", err)
	}
}

// A ciphertext moved onto another record must not open there.
func TestOpenRejectsTransplantedSecret(t *testing.T) {
	e := newEnv(t)
	a := e.addDAVConn("https://a.example/", davPass)
	sealed, err := e.st.ConnectionPassword(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.DB().Exec("UPDATE connections SET name = 'dav-a' WHERE id = ?", a); err != nil {
		t.Fatal(err)
	}
	b := e.addDAVConn("https://b.example/", "")
	if _, err := e.st.DB().Exec("UPDATE connections SET password = ? WHERE id = ?", sealed, b); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.o.Open(context.Background(), b, shortRunDir(t), redact.New()); err == nil {
		t.Error("a password sealed for another connection was accepted")
	}
}

func TestOpenerCheck(t *testing.T) {
	e := newEnv(t)
	keyID, p := e.addKey(keyPass)
	ok := sshtest.Start(t, sshtest.WithAuthorized(p.Signer.PublicKey()))
	if err := e.o.Check(context.Background(), e.addSSHConn(keyID, ok)); err != nil {
		t.Errorf("authorized: %v", err)
	}
	denied := sshtest.Start(t)
	if err := e.o.Check(context.Background(), e.addSSHConn(keyID, denied)); model.KindOf(err) != model.KindAuth {
		t.Errorf("unauthorized: err = %v, want kind auth", err)
	}
}
