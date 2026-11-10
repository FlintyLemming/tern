package vault

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tern/internal/store"
)

const testPass = "correct horse battery staple"

type env struct {
	st      *store.Store
	secrets string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &env{st: st, secrets: t.TempDir()}
}

func (e *env) keyPath() string { return filepath.Join(e.secrets, "master.key") }

// fresh returns a new Vault over the same store, as after a process restart.
func (e *env) fresh(t *testing.T) (*Vault, State) {
	t.Helper()
	v := New(e.st, e.secrets)
	st, err := v.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st != v.State() {
		t.Fatalf("Load returned %q but State() is %q", st, v.State())
	}
	return v, st
}

func (e *env) initVault(t *testing.T, mode Mode) *Vault {
	t.Helper()
	v, st := e.fresh(t)
	if st != StateUninitialized {
		t.Fatalf("state before Init = %q", st)
	}
	pass := ""
	if mode == ModePassphrase {
		pass = testPass
	}
	if err := v.Init(context.Background(), mode, pass); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if v.State() != StateUnlocked || v.Mode() != mode {
		t.Fatalf("after Init state=%q mode=%q", v.State(), v.Mode())
	}
	return v
}

func (e *env) meta(t *testing.T, key string) (string, bool) {
	t.Helper()
	v, ok, err := e.st.GetMeta(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return v, ok
}

func mustSeal(t *testing.T, v *Vault, aad string, p []byte) string {
	t.Helper()
	s, err := v.Seal(aad, p)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	return s
}

func TestSealOpenRoundTrip(t *testing.T) {
	v := newEnv(t).initVault(t, ModeKeyfile)
	p := []byte("-----BEGIN OPENSSH PRIVATE KEY-----")
	s := mustSeal(t, v, "ssh_keys/a/private", p)
	if !strings.HasPrefix(s, "v1.") {
		t.Errorf("sealed value %q lacks the v1. prefix", s)
	}
	got, err := v.Open("ssh_keys/a/private", s)
	if err != nil || !bytes.Equal(got, p) {
		t.Fatalf("Open = (%q, %v)", got, err)
	}
}

func TestSealIsRandomized(t *testing.T) {
	v := newEnv(t).initVault(t, ModeKeyfile)
	a := mustSeal(t, v, "x/1/f", []byte("same"))
	b := mustSeal(t, v, "x/1/f", []byte("same"))
	if a == b {
		t.Fatal("two seals of the same plaintext are identical")
	}
}

func TestOpenRejectsOtherAAD(t *testing.T) {
	v := newEnv(t).initVault(t, ModeKeyfile)
	s := mustSeal(t, v, "ssh_keys/a/private", []byte("p"))
	if _, err := v.Open("ssh_keys/b/private", s); err == nil {
		t.Fatal("Open accepted a ciphertext bound to another record")
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	v := newEnv(t).initVault(t, ModeKeyfile)
	s := mustSeal(t, v, "x/1/f", []byte("payload"))
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, "v1."))
	if err != nil {
		t.Fatal(err)
	}
	for i := range raw {
		mut := bytes.Clone(raw)
		mut[i] ^= 0x01
		if _, err := v.Open("x/1/f", "v1."+base64.RawURLEncoding.EncodeToString(mut)); err == nil {
			t.Fatalf("Open accepted ciphertext with byte %d flipped", i)
		}
	}
	for _, bad := range []string{"", "v1.", "v2." + s[3:], "v1.!!!", s[:len(s)-8]} {
		if _, err := v.Open("x/1/f", bad); err == nil {
			t.Errorf("Open accepted malformed input %q", bad)
		}
	}
}

func TestSealWhenLocked(t *testing.T) {
	e := newEnv(t)
	e.initVault(t, ModePassphrase)
	v, _ := e.fresh(t)
	if _, err := v.Seal("x/1/f", []byte("p")); !errors.Is(err, ErrLocked) {
		t.Errorf("Seal err = %v, want ErrLocked", err)
	}
	if _, err := v.Open("x/1/f", "v1.AAAA"); !errors.Is(err, ErrLocked) {
		t.Errorf("Open err = %v, want ErrLocked", err)
	}
}

func TestInitTwice(t *testing.T) {
	e := newEnv(t)
	v := e.initVault(t, ModeKeyfile)
	if err := v.Init(context.Background(), ModeKeyfile, ""); !errors.Is(err, ErrAlreadyInitialized) {
		t.Errorf("second Init err = %v, want ErrAlreadyInitialized", err)
	}
	// A different instance that never called Load must not re-initialize either.
	if err := New(e.st, e.secrets).Init(context.Background(), ModePassphrase, testPass); !errors.Is(err, ErrAlreadyInitialized) {
		t.Errorf("Init on a fresh instance err = %v, want ErrAlreadyInitialized", err)
	}
}

func TestKeyfileInitCreatesFile(t *testing.T) {
	e := newEnv(t)
	e.initVault(t, ModeKeyfile)
	fi, err := os.Stat(e.keyPath())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != 32 || fi.Mode().Perm() != 0o600 {
		t.Errorf("master.key size=%d mode=%o, want 32 and 600", fi.Size(), fi.Mode().Perm())
	}
}

func TestKeyfileAutoUnlock(t *testing.T) {
	e := newEnv(t)
	s := mustSeal(t, e.initVault(t, ModeKeyfile), "x/1/f", []byte("kept"))
	v, st := e.fresh(t)
	if st != StateUnlocked {
		t.Fatalf("state = %q, want unlocked", st)
	}
	if got, err := v.Open("x/1/f", s); err != nil || string(got) != "kept" {
		t.Fatalf("Open = (%q, %v)", got, err)
	}
}

func TestKeyfileMissing(t *testing.T) {
	e := newEnv(t)
	e.initVault(t, ModeKeyfile)
	if err := os.Remove(e.keyPath()); err != nil {
		t.Fatal(err)
	}
	v, st := e.fresh(t)
	if st != StateKeyfileMissing {
		t.Fatalf("state = %q, want keyfile_missing", st)
	}
	if entries, _ := os.ReadDir(e.secrets); len(entries) != 0 {
		t.Errorf("secrets dir has %d entries after Load, want none", len(entries))
	}
	if _, err := v.Seal("x/1/f", []byte("p")); !errors.Is(err, ErrLocked) {
		t.Errorf("Seal err = %v, want ErrLocked", err)
	}
}

func TestKeyfileMismatch(t *testing.T) {
	e := newEnv(t)
	e.initVault(t, ModeKeyfile)
	other := make([]byte, 32)
	rand.Read(other)
	if err := os.WriteFile(e.keyPath(), other, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, st := e.fresh(t); st != StateKeyfileMismatch {
		t.Fatalf("state = %q, want keyfile_mismatch", st)
	}
	if got, _ := os.ReadFile(e.keyPath()); !bytes.Equal(got, other) {
		t.Error("Load modified the key file")
	}
}

func TestKeyfileWrongShape(t *testing.T) {
	t.Run("31 bytes", func(t *testing.T) {
		e := newEnv(t)
		e.initVault(t, ModeKeyfile)
		short := bytes.Repeat([]byte{7}, 31)
		os.WriteFile(e.keyPath(), short, 0o600)
		if _, st := e.fresh(t); st != StateKeyfileMismatch {
			t.Fatalf("state = %q, want keyfile_mismatch", st)
		}
		if got, _ := os.ReadFile(e.keyPath()); !bytes.Equal(got, short) {
			t.Error("Load modified the key file")
		}
	})
	t.Run("directory", func(t *testing.T) {
		e := newEnv(t)
		e.initVault(t, ModeKeyfile)
		os.Remove(e.keyPath())
		os.Mkdir(e.keyPath(), 0o700)
		if _, st := e.fresh(t); st != StateKeyfileMismatch {
			t.Fatalf("state = %q, want keyfile_mismatch", st)
		}
		if fi, err := os.Stat(e.keyPath()); err != nil || !fi.IsDir() {
			t.Error("Load replaced the directory")
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root can read anything")
		}
		e := newEnv(t)
		e.initVault(t, ModeKeyfile)
		os.Chmod(e.keyPath(), 0)
		if _, st := e.fresh(t); st != StateKeyfileMismatch {
			t.Fatalf("state = %q, want keyfile_mismatch", st)
		}
		if fi, err := os.Stat(e.keyPath()); err != nil || fi.Size() != 32 || fi.Mode().Perm() != 0 {
			t.Error("Load touched the unreadable key file")
		}
	})
}

func TestKeyfileInitReusesExisting(t *testing.T) {
	e := newEnv(t)
	pre := make([]byte, 32)
	rand.Read(pre)
	os.WriteFile(e.keyPath(), pre, 0o600)
	v := e.initVault(t, ModeKeyfile)
	if got, _ := os.ReadFile(e.keyPath()); !bytes.Equal(got, pre) {
		t.Fatal("Init overwrote an existing key file")
	}
	s := mustSeal(t, v, "x/1/f", []byte("p"))
	v2, st := e.fresh(t)
	if st != StateUnlocked {
		t.Fatalf("state = %q", st)
	}
	if got, err := v2.Open("x/1/f", s); err != nil || string(got) != "p" {
		t.Fatalf("Open = (%q, %v)", got, err)
	}
}

func TestKeyfileInitRefusesWrongShape(t *testing.T) {
	e := newEnv(t)
	short := bytes.Repeat([]byte{7}, 31)
	os.WriteFile(e.keyPath(), short, 0o600)
	v, _ := e.fresh(t)
	if err := v.Init(context.Background(), ModeKeyfile, ""); err == nil {
		t.Fatal("Init accepted a 31-byte key file")
	}
	if got, _ := os.ReadFile(e.keyPath()); !bytes.Equal(got, short) {
		t.Error("Init modified the key file")
	}
	if _, ok := e.meta(t, "vault.mode"); ok {
		t.Error("failed Init left vault.mode behind")
	}
	if v.State() != StateUninitialized {
		t.Errorf("state = %q, want uninitialized", v.State())
	}
}

func TestPassphraseLockedAfterLoad(t *testing.T) {
	e := newEnv(t)
	e.initVault(t, ModePassphrase)
	v, st := e.fresh(t)
	if st != StateLocked || v.Mode() != ModePassphrase {
		t.Fatalf("state=%q mode=%q, want locked/passphrase", st, v.Mode())
	}
	if entries, _ := os.ReadDir(e.secrets); len(entries) != 0 {
		t.Error("passphrase mode created a file in the secrets dir")
	}
}

func TestPassphraseUnlock(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	s := mustSeal(t, e.initVault(t, ModePassphrase), "x/1/f", []byte("kept"))
	v, _ := e.fresh(t)
	if err := v.Unlock(ctx, "wrong passphrase!"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("Unlock(wrong) err = %v, want ErrWrongPassphrase", err)
	}
	if v.State() != StateLocked {
		t.Fatalf("state after wrong passphrase = %q", v.State())
	}
	if err := v.Unlock(ctx, strings.Repeat("x", 1025)); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("Unlock(huge) err = %v, want ErrWrongPassphrase", err)
	}
	if err := v.Unlock(ctx, testPass); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if v.State() != StateUnlocked {
		t.Fatalf("state = %q, want unlocked", v.State())
	}
	if got, err := v.Open("x/1/f", s); err != nil || string(got) != "kept" {
		t.Fatalf("Open = (%q, %v)", got, err)
	}
}

func TestKDFParamsStored(t *testing.T) {
	e := newEnv(t)
	e.initVault(t, ModePassphrase)
	raw, ok := e.meta(t, "vault.kdf")
	if !ok {
		t.Fatal("vault.kdf missing")
	}
	var k struct {
		Salt      string `json:"salt"`
		Time      int    `json:"time"`
		MemoryKiB int    `json:"memory_kib"`
		Threads   int    `json:"threads"`
	}
	if err := json.Unmarshal([]byte(raw), &k); err != nil {
		t.Fatal(err)
	}
	if k.Time != 3 || k.MemoryKiB != 65536 || k.Threads != 4 {
		t.Errorf("kdf params = %+v", k)
	}
	salt, err := base64.StdEncoding.DecodeString(k.Salt)
	if err != nil || len(salt) != 16 {
		t.Errorf("salt decodes to %d bytes (%v), want 16", len(salt), err)
	}
}

func TestSwitchKeyfileToPassphrase(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	v := e.initVault(t, ModeKeyfile)
	s := mustSeal(t, v, "x/1/f", []byte("kept"))
	before, _ := e.meta(t, "vault.dek")
	if err := v.SwitchMode(ctx, ModePassphrase, testPass); err != nil {
		t.Fatalf("SwitchMode: %v", err)
	}
	if got, err := v.Open("x/1/f", s); err != nil || string(got) != "kept" {
		t.Fatalf("Open after switch = (%q, %v)", got, err)
	}
	if after, _ := e.meta(t, "vault.dek"); after == before {
		t.Error("vault.dek did not change")
	}
	v2, st := e.fresh(t)
	if st != StateLocked {
		t.Fatalf("fresh state = %q, want locked", st)
	}
	if err := v2.Unlock(ctx, testPass); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if got, err := v2.Open("x/1/f", s); err != nil || string(got) != "kept" {
		t.Fatalf("Open after unlock = (%q, %v)", got, err)
	}
}

func TestSwitchPassphraseToKeyfile(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	v := e.initVault(t, ModePassphrase)
	s := mustSeal(t, v, "x/1/f", []byte("kept"))
	if err := v.SwitchMode(ctx, ModeKeyfile, ""); err != nil {
		t.Fatalf("SwitchMode: %v", err)
	}
	if _, ok := e.meta(t, "vault.kdf"); ok {
		t.Error("vault.kdf still present in keyfile mode")
	}
	v2, st := e.fresh(t)
	if st != StateUnlocked {
		t.Fatalf("fresh state = %q, want unlocked", st)
	}
	if got, err := v2.Open("x/1/f", s); err != nil || string(got) != "kept" {
		t.Fatalf("Open = (%q, %v)", got, err)
	}
	// A locked vault cannot be switched: the data key is not in memory.
	e2 := newEnv(t)
	e2.initVault(t, ModePassphrase)
	locked, _ := e2.fresh(t)
	if err := locked.SwitchMode(ctx, ModeKeyfile, ""); !errors.Is(err, ErrLocked) {
		t.Errorf("SwitchMode on locked vault err = %v, want ErrLocked", err)
	}
}
