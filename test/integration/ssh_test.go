//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"tern/internal/model"
	"tern/internal/sshx"
	kit "tern/test/testkit"
)

func TestToolVersions(t *testing.T) {
	for tool, want := range map[string]string{"restic": "0.19.1", "rclone": "v1.76.0"} {
		out, err := exec.Command(tool, "version").CombinedOutput()
		if err != nil || !strings.Contains(string(out), want) {
			t.Errorf("%s version: %v\n%s\nwant it to contain %q", tool, err, out, want)
		}
	}
	for _, argv := range [][]string{{"rsync", "--version"}, {"ssh", "-V"}} {
		if out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil {
			t.Errorf("%v: %v\n%s", argv, err, out)
		}
	}
}

func TestRealSSHWithAgent(t *testing.T) {
	cfg := kit.OpenSSH(t, kit.Authorize(t), kit.Target(t))
	stdout, stderr, code := kit.RunSSH(t, cfg, "echo", "ok")
	if code != 0 || stdout != "ok\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// RSA keys make OpenSSH ask the agent for an rsa-sha2 signature through the
// flags extension; the sign-only agent has to pass that through.
func TestRealSSHWithRSAKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := sshx.ParsePrivate(pem.EncodeToMemory(blk), nil)
	if err != nil {
		t.Fatal(err)
	}
	kit.AppendAuthorized(t, p)
	stdout, stderr, code := kit.RunSSH(t, kit.OpenSSH(t, p, kit.Target(t)), "echo", "ok")
	if code != 0 || stdout != "ok\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestRealSSHWrongHostKey(t *testing.T) {
	tg := kit.Target(t)
	tg.HostKey = kit.NewKey(t).Authorized
	_, stderr, code := kit.RunSSH(t, kit.OpenSSH(t, kit.Authorize(t), tg), "echo", "ok")
	if code != 255 || !strings.Contains(stderr, "Host key verification failed") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

func TestRealSSHUnauthorizedKey(t *testing.T) {
	_, stderr, code := kit.RunSSH(t, kit.OpenSSH(t, kit.NewKey(t), kit.Target(t)), "echo", "ok")
	if code != 255 || !strings.Contains(stderr, "Permission denied") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

func TestRealSSHNoKeyMaterial(t *testing.T) {
	cfg := kit.OpenSSH(t, kit.Authorize(t), kit.Target(t))
	dir := filepath.Dir(cfg)
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("ssh", "-F", cfg, sshx.Alias, "echo started; sleep 2")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()

	// Wait until the session is really up, so the inspection below looks at
	// ssh while it is authenticated and working.
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(stdout.String(), "started") {
		if time.Now().After(deadline) {
			t.Fatalf("ssh never started: stderr %q", stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		if e.Type().IsRegular() {
			if b, _ := os.ReadFile(filepath.Join(dir, e.Name())); bytes.Contains(b, []byte("PRIVATE KEY")) {
				t.Errorf("%s contains private key material", e.Name())
			}
		}
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "agent.sock,known_hosts,ssh_config" {
		t.Errorf("run directory holds %v", names)
	}
	for _, f := range []string{"cmdline", "environ"} {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/%s", cmd.Process.Pid, f))
		if err != nil {
			t.Fatalf("read /proc/<ssh>/%s: %v", f, err)
		}
		if len(b) == 0 {
			t.Errorf("/proc/<ssh>/%s is empty; the check would prove nothing", f)
		}
		if bytes.Contains(b, []byte("PRIVATE KEY")) {
			t.Errorf("/proc/<ssh>/%s contains private key material", f)
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Errorf("ssh: %v, stderr %q", err, stderr.String())
	}
}

func TestRealCheck(t *testing.T) {
	ctx := context.Background()
	p, tg := kit.Authorize(t), kit.Target(t)
	if err := sshx.Check(ctx, tg, p.Signer); err != nil {
		t.Errorf("authorized key, right host key: %v", err)
	}
	wrong := tg
	wrong.HostKey = kit.NewKey(t).Authorized
	if err := sshx.Check(ctx, wrong, p.Signer); model.KindOf(err) != model.KindHostKey {
		t.Errorf("wrong host key: err = %v, want kind host_key", err)
	}
	if err := sshx.Check(ctx, tg, kit.NewKey(t).Signer); model.KindOf(err) != model.KindAuth {
		t.Errorf("unauthorized key: err = %v, want kind auth", err)
	}
}

func TestRealSSHIPv6(t *testing.T) {
	ips, err := net.LookupIP(os.Getenv("TERN_ITEST_SSH_HOST"))
	if err != nil {
		t.Fatal(err)
	}
	var v6 string
	for _, ip := range ips {
		if ip.To4() == nil {
			v6 = ip.String()
		}
	}
	if v6 == "" {
		t.Fatalf("sshd has no IPv6 address (%v); the compose network must enable IPv6", ips)
	}
	tg := kit.Target(t)
	tg.Host = v6
	hk, err := sshx.ProbeHostKey(context.Background(), v6, tg.Port)
	if err != nil || hk.Authorized != tg.HostKey {
		t.Fatalf("probe over IPv6: %v (same key: %v)", err, hk.Authorized == tg.HostKey)
	}
	p := kit.Authorize(t)
	if err := sshx.Check(context.Background(), tg, p.Signer); err != nil {
		t.Errorf("Check over IPv6: %v", err)
	}
	stdout, stderr, code := kit.RunSSH(t, kit.OpenSSH(t, p, tg), "echo", "ok")
	if code != 0 || stdout != "ok\n" {
		t.Fatalf("ssh to %s: exit %d, stdout %q, stderr %q", v6, code, stdout, stderr)
	}
}

func TestRemotePathIsFreshAndWritable(t *testing.T) {
	a, b := kit.RemotePath(t), kit.RemotePath(t)
	if a == b || !strings.HasPrefix(a, "/srv/") {
		t.Fatalf("remotePath = %q, %q", a, b)
	}
	cfg := kit.OpenSSH(t, kit.Authorize(t), kit.Target(t))
	if _, stderr, code := kit.RunSSH(t, cfg, "touch", a+"/probe", "&&", "test", "-z", `"$(ls `+b+`)"`); code != 0 {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

func TestFetchToolsRejectsBadHash(t *testing.T) {
	script, err := os.ReadFile("../../deploy/fetch-tools.sh")
	if err != nil {
		t.Fatal(err)
	}
	// Flip one digit of the restic hash for this architecture.
	const marker = "RESTIC_SHA256_"
	i := bytes.Index(script, []byte(marker+runtime.GOARCH+"="))
	if i < 0 {
		t.Fatalf("no %s%s in the script", marker, runtime.GOARCH)
	}
	at := i + len(marker) + len(runtime.GOARCH) + 1
	tampered := bytes.Clone(script)
	if tampered[at] == '0' {
		tampered[at] = '1'
	} else {
		tampered[at] = '0'
	}
	work := t.TempDir()
	path := filepath.Join(work, "fetch-tools.sh")
	if err := os.WriteFile(path, tampered, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(work, "bin")
	cmd := exec.Command("sh", path, dest)
	cmd.Env = append(os.Environ(), "TARGETARCH="+runtime.GOARCH)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("tampered script succeeded:\n%s", out)
	}
	if !strings.Contains(string(out), "SHA256 mismatch") {
		t.Errorf("failed for another reason:\n%s", out)
	}
	for _, tool := range []string{"restic", "rclone"} {
		if _, err := os.Stat(filepath.Join(dest, tool)); !os.IsNotExist(err) {
			t.Errorf("%s was installed despite the bad hash (stat: %v)", tool, err)
		}
	}

	cmd = exec.Command("sh", path, dest)
	cmd.Env = append(os.Environ(), "TARGETARCH=riscv64")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("unsupported architecture accepted:\n%s", out)
	}
}
