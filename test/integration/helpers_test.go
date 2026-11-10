//go:build integration

// Package integration holds tests that need the real tools and a real remote
// end. They run inside the compose environment: `make itest`.
package integration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"tern/internal/model"
	"tern/internal/rundir"
	"tern/internal/sshx"
)

func env(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("%s is not set; run these tests with `make itest`", key)
	}
	return v
}

// newKey generates a key pair the remote end knows nothing about.
func newKey(t *testing.T) sshx.Parsed {
	t.Helper()
	_, p, err := sshx.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var authorizedMu sync.Mutex

// appendAuthorized adds p's public key to the authorized_keys file sshd
// reads, through the volume both containers share.
func appendAuthorized(t *testing.T, p sshx.Parsed) {
	t.Helper()
	authorizedMu.Lock()
	defer authorizedMu.Unlock()
	f, err := os.OpenFile("/keys/authorized_keys", os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(p.Authorized + "\n"); err != nil {
		t.Fatal(err)
	}
}

// authorize generates a key and lets it log in to the remote end.
func authorize(t *testing.T) sshx.Parsed {
	t.Helper()
	p := newKey(t)
	appendAuthorized(t, p)
	return p
}

// target is the remote sshd, with the host key it presents right now.
func target(t *testing.T) sshx.Target {
	t.Helper()
	tg := sshx.Target{Host: env(t, "TERN_ITEST_SSH_HOST"), Port: 22, User: env(t, "TERN_ITEST_SSH_USER")}
	hk, err := sshx.ProbeHostKey(context.Background(), tg.Host, tg.Port)
	if err != nil {
		t.Fatalf("probe %s: %v", tg.Host, err)
	}
	tg.HostKey = hk.Authorized
	return tg
}

// openSSH does what access.Opener does for an SSH connection — agent plus
// generated ssh_config in a tmpfs run directory — and returns the config
// path. Use it as: ssh -F <cfgPath> tern-target.
func openSSH(t *testing.T, p sshx.Parsed, tg sshx.Target) (cfgPath string) {
	t.Helper()
	base, err := os.MkdirTemp("/dev/shm", "ti")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	if err := rundir.VerifyTmpfs(base); err != nil {
		t.Fatal(err)
	}
	dir, err := rundir.New(base, "r")
	if err != nil {
		t.Fatal(err)
	}
	ag, err := sshx.StartAgent(dir, p.Raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ag.Close() })
	cfgPath, err = sshx.WriteConfig(dir, tg, ag.Socket())
	if err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

// runSSH runs a command on the remote end through real OpenSSH.
func runSSH(t *testing.T, cfgPath string, remoteCmd ...string) (stdout, stderr string, exit int) {
	t.Helper()
	var out, errb bytes.Buffer
	cmd := exec.Command("ssh", "-F", cfgPath, sshx.Alias, strings.Join(remoteCmd, " "))
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		exit = ee.ExitCode()
	default:
		t.Fatalf("run ssh: %v", err)
	}
	return out.String(), errb.String(), exit
}

// remotePath creates and returns a new directory under /srv on the remote
// end, so tests never see each other's files.
func remotePath(t *testing.T) string {
	t.Helper()
	path := filepath.Join("/srv", "t-"+model.NewID())
	if _, stderr, code := runSSH(t, openSSH(t, authorize(t), target(t)), "mkdir", path); code != 0 {
		t.Fatalf("create %s on the remote end: exit %d, %s", path, code, stderr)
	}
	return path
}
