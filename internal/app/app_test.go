package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tern/internal/config"
	"tern/internal/store"
)

func testCfg(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Listen:     "127.0.0.1:0",
		DataDir:    filepath.Join(t.TempDir(), "data"),
		SecretsDir: t.TempDir(),
		RuntimeDir: t.TempDir(),
	}
}

func start(t *testing.T, cfg config.Config) *App {
	t.Helper()
	a, err := Start(context.Background(), cfg, "test")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { a.Shutdown(context.Background()) })
	return a
}

func getStatus(t *testing.T, c *http.Client, url string) map[string]any {
	t.Helper()
	resp, err := c.Get(url + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestStartServesStatus(t *testing.T) {
	a := start(t, testCfg(t))
	if a.API() == nil {
		t.Fatal("API() is nil")
	}
	s := getStatus(t, http.DefaultClient, "http://"+a.Addr())
	if s["setup_required"] != true || s["version"] != "test" {
		t.Fatalf("status = %v", s)
	}
}

func TestShutdownReleasesLock(t *testing.T) {
	cfg := testCfg(t)
	a := start(t, cfg)
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	start(t, cfg)
}

func TestStartFailsOnLockedDataDir(t *testing.T) {
	cfg := testCfg(t)
	start(t, cfg)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	cfg.Listen = addr
	b, err := Start(context.Background(), cfg, "test")
	if !errors.Is(err, store.ErrLockHeld) {
		if b != nil {
			b.Shutdown(context.Background())
		}
		t.Fatalf("second Start err = %v, want ErrLockHeld", err)
	}
	ln, err = net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("the failed Start kept %s: %v", addr, err)
	}
	ln.Close()
}

func TestStartCreatesDataDir0700(t *testing.T) {
	cfg := testCfg(t)
	start(t, cfg)
	fi, err := os.Stat(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("data dir mode = %o, want 700", fi.Mode().Perm())
	}
}

func TestTLS(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "tern-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
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
	cfg := testCfg(t)
	cfg.TLSCert = filepath.Join(dir, "cert.pem")
	cfg.TLSKey = filepath.Join(dir, "key.pem")
	os.WriteFile(cfg.TLSCert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(cfg.TLSKey, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)

	a := start(t, cfg)
	pool := x509.NewCertPool()
	cert, _ := x509.ParseCertificate(der)
	pool.AddCert(cert)
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	if s := getStatus(t, c, "https://"+a.Addr()); s["setup_required"] != true {
		t.Fatalf("status = %v", s)
	}
}

func TestStartFailsOnBadTLSFiles(t *testing.T) {
	cfg := testCfg(t)
	cfg.TLSCert = filepath.Join(t.TempDir(), "missing.pem")
	cfg.TLSKey = cfg.TLSCert
	if a, err := Start(context.Background(), cfg, "test"); err == nil {
		a.Shutdown(context.Background())
		t.Fatal("Start succeeded with missing TLS files")
	}
	// The failed Start released the data directory.
	cfg.TLSCert, cfg.TLSKey = "", ""
	start(t, cfg)
}
