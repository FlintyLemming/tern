package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"tern/internal/app"
	"tern/internal/config"
	"tern/internal/proc"
	"tern/internal/runner"
)

// testEnv is the environment of a node with fresh directories, listening on
// a free loopback port.
func testEnv(t *testing.T) map[string]string {
	t.Helper()
	runDir := t.TempDir()
	if runtime.GOOS == "linux" { // the runtime directory must be a tmpfs there
		d, err := os.MkdirTemp("/dev/shm", "tern-test")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(d) })
		runDir = d
	}
	return map[string]string{
		"TERN_LISTEN":      "127.0.0.1:0",
		"TERN_DATA_DIR":    filepath.Join(t.TempDir(), "data"),
		"TERN_SECRETS_DIR": t.TempDir(),
		"TERN_RUNTIME_DIR": runDir,
	}
}

func getenv(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

// startNode starts a node from env and points TERN_LISTEN at the address it
// actually listens on.
func startNode(t *testing.T, env map[string]string) {
	t.Helper()
	cfg, err := config.Load(getenv(env))
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.Start(context.Background(), cfg, "test")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { a.Shutdown(context.Background()) })
	env["TERN_LISTEN"] = a.Addr()
}

func TestHealthcheckOK(t *testing.T) {
	env := testEnv(t)
	startNode(t, env)
	if code := healthcheck(getenv(env)); code != 0 {
		t.Fatalf("healthcheck = %d, want 0", code)
	}
	// The image listens on ":8080": no host means this machine.
	_, port, _ := net.SplitHostPort(env["TERN_LISTEN"])
	for _, listen := range []string{":" + port, "0.0.0.0:" + port} {
		env["TERN_LISTEN"] = listen
		if code := healthcheck(getenv(env)); code != 0 {
			t.Fatalf("healthcheck with TERN_LISTEN=%s = %d, want 0", listen, code)
		}
	}
}

// The QNAP package serves tern below /tern; the check has to ask there.
func TestHealthcheckBasePath(t *testing.T) {
	env := testEnv(t)
	env["TERN_BASE_PATH"] = "/tern"
	startNode(t, env)
	if code := healthcheck(getenv(env)); code != 0 {
		t.Fatalf("healthcheck with TERN_BASE_PATH = %d, want 0", code)
	}
	delete(env, "TERN_BASE_PATH") // asking at the root finds nothing
	if code := healthcheck(getenv(env)); code != 1 {
		t.Fatalf("healthcheck without the base path = %d, want 1", code)
	}
}

func TestHealthcheckDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens there any more

	env := testEnv(t)
	env["TERN_LISTEN"] = addr
	if code := healthcheck(getenv(env)); code != 1 {
		t.Fatalf("healthcheck = %d, want 1", code)
	}
}

func TestHealthcheckTLS(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "nas.example"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"nas.example"}, // not the loopback address
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	env := testEnv(t)
	env["TERN_TLS_CERT"] = filepath.Join(dir, "cert.pem")
	env["TERN_TLS_KEY"] = filepath.Join(dir, "key.pem")
	os.WriteFile(env["TERN_TLS_CERT"], pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(env["TERN_TLS_KEY"], pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	startNode(t, env)

	if code := healthcheck(getenv(env)); code != 0 {
		t.Fatalf("healthcheck = %d, want 0", code)
	}
}

// The stop budget of plan 7: docker's grace period (compose: 2m) > the
// process's own wind-down (100 s) > child kill grace (30 s) + post-task
// command on shutdown (60 s).
func TestShutdownBudget(t *testing.T) {
	const dockerGrace = 2 * time.Minute
	if shutdownBudget != 100*time.Second {
		t.Errorf("shutdownBudget = %s, want 100s", shutdownBudget)
	}
	if shutdownBudget >= dockerGrace {
		t.Errorf("shutdownBudget %s does not fit docker's grace period %s", shutdownBudget, dockerGrace)
	}
	if need := proc.KillGrace + runner.ShutdownHookCap; shutdownBudget <= need {
		t.Errorf("shutdownBudget %s leaves no room after kill grace and post-task command (%s)", shutdownBudget, need)
	}
}
