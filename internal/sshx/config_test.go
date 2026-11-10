package sshx

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func rsaParsed(t *testing.T) Parsed {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePrivate(marshal(t, k, ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func ecdsaParsed(t *testing.T) Parsed {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePrivate(marshal(t, k, ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

const goldenConfig = `Host tern-target
  HostName <host>
  Port <port>
  User <user>
  HostKeyAlias tern-target
  UserKnownHostsFile <dir>/known_hosts
  GlobalKnownHostsFile /dev/null
  StrictHostKeyChecking yes
  HostKeyAlgorithms <algos>
  IdentityAgent <dir>/agent.sock
  IdentityFile none
  PubkeyAuthentication yes
  PasswordAuthentication no
  KbdInteractiveAuthentication no
  BatchMode yes
  ServerAliveInterval 15
  ServerAliveCountMax 4
  ConnectTimeout 15
  ControlMaster no
  ForwardAgent no
  LogLevel ERROR
`

func golden(host, port, user, dir, algos string) string {
	return strings.NewReplacer("<host>", host, "<port>", port, "<user>", user, "<dir>", dir, "<algos>", algos).Replace(goldenConfig)
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWriteConfigGolden(t *testing.T) {
	dir := shortRunDir(t)
	hostKey := genParsed(t).Authorized
	path, err := WriteConfig(dir, Target{Host: "nas2.example", Port: 2222, User: "backup", HostKey: hostKey}, filepath.Join(dir.Path, "agent.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir.Path, "ssh_config") {
		t.Errorf("path = %q", path)
	}
	if got, want := read(t, path), golden("nas2.example", "2222", "backup", dir.Path, "ssh-ed25519"); got != want {
		t.Errorf("ssh_config:\n%s\nwant:\n%s", got, want)
	}
	if got, want := read(t, filepath.Join(dir.Path, "known_hosts")), Alias+" "+hostKey+"\n"; got != want {
		t.Errorf("known_hosts = %q, want %q", got, want)
	}
	if Alias != "tern-target" {
		t.Errorf("Alias = %q", Alias)
	}
}

func TestWriteConfigAlgos(t *testing.T) {
	for want, p := range map[string]Parsed{
		"ssh-ed25519":               genParsed(t),
		"rsa-sha2-512,rsa-sha2-256": rsaParsed(t),
		"ecdsa-sha2-nistp256":       ecdsaParsed(t),
	} {
		dir := shortRunDir(t)
		path, err := WriteConfig(dir, Target{Host: "h", Port: 22, User: "u", HostKey: p.Authorized}, filepath.Join(dir.Path, "agent.sock"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(read(t, path), "\n  HostKeyAlgorithms "+want+"\n") {
			t.Errorf("want HostKeyAlgorithms %s in:\n%s", want, read(t, path))
		}
		kh := read(t, filepath.Join(dir.Path, "known_hosts"))
		if !strings.HasPrefix(kh, "tern-target "+p.Signer.PublicKey().Type()+" ") || strings.Count(kh, "\n") != 1 {
			t.Errorf("known_hosts = %q", kh)
		}
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimPrefix(kh, "tern-target "))); err != nil {
			t.Errorf("known_hosts key does not parse: %v", err)
		}
	}
}

func TestWriteConfigIPv6(t *testing.T) {
	dir := shortRunDir(t)
	path, err := WriteConfig(dir, Target{Host: "fd7a:115c::1", Port: 22, User: "u", HostKey: genParsed(t).Authorized}, filepath.Join(dir.Path, "agent.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, path), "\n  HostName fd7a:115c::1\n") {
		t.Errorf("config:\n%s", read(t, path))
	}
}

func TestWriteConfigFileModes(t *testing.T) {
	dir := shortRunDir(t)
	path, err := WriteConfig(dir, Target{Host: "h", Port: 22, User: "u", HostKey: genParsed(t).Authorized}, filepath.Join(dir.Path, "agent.sock"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, filepath.Join(dir.Path, "known_hosts")} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o", p, fi.Mode().Perm())
		}
	}
}

// The API validates before saving; WriteConfig still refuses on its own, so a
// row edited by hand in the database cannot smuggle options into ssh_config.
func TestWriteConfigRefusesInjection(t *testing.T) {
	hostKey := genParsed(t).Authorized
	bad := []Target{
		{Host: "a\n  ProxyCommand x", Port: 22, User: "u", HostKey: hostKey},
		{Host: "-oProxyCommand=x", Port: 22, User: "u", HostKey: hostKey},
		{Host: "a b", Port: 22, User: "u", HostKey: hostKey},
		{Host: "h", Port: 22, User: "u\n  ProxyCommand x", HostKey: hostKey},
		{Host: "h", Port: 22, User: "-x", HostKey: hostKey},
		{Host: "h", Port: 22, User: "", HostKey: hostKey},
		{Host: "h", Port: 0, User: "u", HostKey: hostKey},
		{Host: "h", Port: 22, User: "u", HostKey: "garbage"},
		{Host: "h", Port: 22, User: "u", HostKey: hostKey + "\nssh-ed25519 AAAA"},
	}
	for _, tg := range bad {
		dir := shortRunDir(t)
		if _, err := WriteConfig(dir, tg, filepath.Join(dir.Path, "agent.sock")); err == nil {
			t.Errorf("WriteConfig accepted %+v", tg)
		}
		if entries, _ := os.ReadDir(dir.Path); len(entries) != 0 {
			t.Errorf("files written for refused target %+v: %v", tg, entries)
		}
	}
}
