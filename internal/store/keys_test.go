package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"tern/internal/model"
)

func sampleKey(id, name string) model.SSHKey {
	at := time.Unix(1_700_000_000, 0).UTC()
	return model.SSHKey{ID: id, Version: 1, Name: name, PublicKey: "ssh-ed25519 AAAA" + id, Fingerprint: "SHA256:" + id,
		HasPassphrase: true, CreatedAt: at, UpdatedAt: at}
}

func TestKeyRoundTrip(t *testing.T) {
	s, ctx := open(t, t.TempDir()), context.Background()
	want := sampleKey("k1", "nas key")
	if err := s.CreateKey(ctx, want, "v1.priv", "v1.pass"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetKey(ctx, "k1")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("GetKey = %+v, want %+v", got, want)
	}
	priv, pass, err := s.KeySecrets(ctx, "k1")
	if err != nil || priv != "v1.priv" || pass != "v1.pass" {
		t.Errorf("KeySecrets = %q, %q, %v", priv, pass, err)
	}
	second := sampleKey("k2", "other")
	second.HasPassphrase = false
	if err := s.CreateKey(ctx, second, "v1.priv2", ""); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListKeys(ctx)
	if err != nil || len(list) != 2 || list[0] != want || list[1] != second {
		t.Errorf("ListKeys = %+v, %v", list, err)
	}
}

func TestKeyNotFound(t *testing.T) {
	s, ctx := open(t, t.TempDir()), context.Background()
	if _, err := s.GetKey(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetKey err = %v", err)
	}
	if _, _, err := s.KeySecrets(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("KeySecrets err = %v", err)
	}
	if err := s.RenameKey(ctx, "nope", 1, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("RenameKey err = %v", err)
	}
	if err := s.DeleteKey(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteKey err = %v", err)
	}
}

func TestKeyDuplicateName(t *testing.T) {
	s, ctx := open(t, t.TempDir()), context.Background()
	if err := s.CreateKey(ctx, sampleKey("k1", "same"), "p", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateKey(ctx, sampleKey("k2", "same"), "p", ""); !errors.Is(err, ErrDuplicate) {
		t.Errorf("CreateKey err = %v, want ErrDuplicate", err)
	}
	if err := s.CreateKey(ctx, sampleKey("k2", "two"), "p", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameKey(ctx, "k2", 1, "same"); !errors.Is(err, ErrDuplicate) {
		t.Errorf("RenameKey err = %v, want ErrDuplicate", err)
	}
}

func TestRenameKeyVersion(t *testing.T) {
	s, ctx := open(t, t.TempDir()), context.Background()
	if err := s.CreateKey(ctx, sampleKey("k1", "a"), "p", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameKey(ctx, "k1", 1, "b"); err != nil {
		t.Fatal(err)
	}
	k, _ := s.GetKey(ctx, "k1")
	if k.Name != "b" || k.Version != 2 || !k.UpdatedAt.After(k.CreatedAt) {
		t.Errorf("after rename: %+v", k)
	}
	if err := s.RenameKey(ctx, "k1", 1, "c"); !errors.Is(err, ErrVersion) {
		t.Errorf("stale rename err = %v, want ErrVersion", err)
	}
}

func TestDeleteKey(t *testing.T) {
	s, ctx := open(t, t.TempDir()), context.Background()
	if err := s.CreateKey(ctx, sampleKey("k1", "a"), "p", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO connections (id, version, type, name, ssh_key_id, created_at, updated_at)
		VALUES ('c1', 1, 'ssh', 'c', 'k1', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteKey(ctx, "k1"); !errors.Is(err, ErrInUse) {
		t.Fatalf("DeleteKey err = %v, want ErrInUse", err)
	}
	if _, err := s.DB().Exec(`DELETE FROM connections`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteKey(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetKey(ctx, "k1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("key still there: %v", err)
	}
}
