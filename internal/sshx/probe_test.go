package sshx

import (
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"tern/internal/model"
	"tern/internal/sshx/sshtest"
)

// freePort returns a loopback port nothing is listening on.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func needIPv6(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback here: %v", err)
	}
	ln.Close()
}

func TestProbeReturnsServerKey(t *testing.T) {
	srv := sshtest.Start(t)
	hk, err := ProbeHostKey(context.Background(), srv.Host(), srv.Port())
	if err != nil {
		t.Fatal(err)
	}
	if hk.Fingerprint != ssh.FingerprintSHA256(srv.HostPublicKey()) {
		t.Errorf("Fingerprint = %q", hk.Fingerprint)
	}
	if hk.Type != "ssh-ed25519" || hk.Authorized != AuthorizedKey(srv.HostPublicKey()) {
		t.Errorf("HostKey = %+v", hk)
	}
}

func TestProbeIPv6(t *testing.T) {
	needIPv6(t)
	srv := sshtest.Start(t, sshtest.WithListen("[::1]:0"))
	hk, err := ProbeHostKey(context.Background(), "::1", srv.Port())
	if err != nil {
		t.Fatal(err)
	}
	if hk.Fingerprint != ssh.FingerprintSHA256(srv.HostPublicKey()) {
		t.Errorf("Fingerprint = %q", hk.Fingerprint)
	}
}

func TestProbeRefused(t *testing.T) {
	_, err := ProbeHostKey(context.Background(), "127.0.0.1", freePort(t))
	if k := model.KindOf(err); k != model.KindNetwork {
		t.Fatalf("err = %v (kind %q), want network", err, k)
	}
}

func TestProbeHangTimesOut(t *testing.T) {
	srv := sshtest.Start(t, sshtest.WithHang())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	_, err := ProbeHostKey(ctx, srv.Host(), srv.Port())
	if k := model.KindOf(err); k != model.KindNetwork {
		t.Errorf("err = %v (kind %q), want network", err, k)
	}
	if d := time.Since(start); d >= 3*time.Second {
		t.Errorf("took %v", d)
	}
}
