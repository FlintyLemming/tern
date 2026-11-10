package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"tern/internal/model"
)

const sampleHostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"

func TestConnectionRoundTrip(t *testing.T) {
	s, ctx := open(t, t.TempDir()), context.Background()
	at := time.Unix(1_700_000_000, 0).UTC()
	if err := s.CreateKey(ctx, sampleKey("k1", "key"), "p", ""); err != nil {
		t.Fatal(err)
	}
	sshc := model.Connection{ID: "c1", Version: 1, Type: "ssh", Name: "nas2", Host: "fd7a::1", Port: 2222, Username: "backup",
		SSHKeyID: "k1", HostKey: sampleHostKey, CreatedAt: at, UpdatedAt: at}
	dav := model.Connection{ID: "c2", Version: 1, Type: "webdav", Name: "dav", URL: "https://h/dav", Username: "me",
		CreatedAt: at.Add(time.Second), UpdatedAt: at.Add(time.Second)}
	if err := s.CreateConnection(ctx, sshc, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateConnection(ctx, dav, "v1.sealed"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetConnection(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if got.HostKeyFingerprint == "" || got.HostKeyFingerprint[:7] != "SHA256:" {
		t.Errorf("HostKeyFingerprint = %q", got.HostKeyFingerprint)
	}
	sshc.HostKeyFingerprint = got.HostKeyFingerprint
	if got != sshc {
		t.Errorf("GetConnection = %+v, want %+v", got, sshc)
	}
	dav.HasPassword = true
	list, err := s.ListConnections(ctx)
	if err != nil || len(list) != 2 || list[0] != sshc || list[1] != dav {
		t.Errorf("ListConnections = %+v, %v", list, err)
	}
	if pw, err := s.ConnectionPassword(ctx, "c2"); err != nil || pw != "v1.sealed" {
		t.Errorf("ConnectionPassword = %q, %v", pw, err)
	}
	if err := s.CreateConnection(ctx, model.Connection{ID: "c3", Version: 1, Type: "webdav", Name: "dav"}, ""); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate name err = %v", err)
	}
}

func TestUpdateConnection(t *testing.T) {
	s, ctx := open(t, t.TempDir()), context.Background()
	at := time.Unix(1_700_000_000, 0).UTC()
	dav := model.Connection{ID: "c2", Version: 1, Type: "webdav", Name: "dav", URL: "https://h/dav", Username: "me", CreatedAt: at, UpdatedAt: at}
	if err := s.CreateConnection(ctx, dav, "v1.old"); err != nil {
		t.Fatal(err)
	}
	dav.Name, dav.URL, dav.UpdatedAt = "dav2", "https://h/other", at.Add(time.Hour)
	if err := s.UpdateConnection(ctx, dav, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetConnection(ctx, "c2")
	if got.Name != "dav2" || got.URL != "https://h/other" || got.Version != 2 || !got.UpdatedAt.Equal(at.Add(time.Hour)) || !got.CreatedAt.Equal(at) {
		t.Errorf("after update: %+v", got)
	}
	if pw, _ := s.ConnectionPassword(ctx, "c2"); pw != "v1.old" {
		t.Errorf("password changed to %q by a nil update", pw)
	}
	if err := s.UpdateConnection(ctx, dav, nil); !errors.Is(err, ErrVersion) {
		t.Errorf("stale update err = %v, want ErrVersion", err)
	}
	dav.Version = 2
	fresh := "v1.new"
	if err := s.UpdateConnection(ctx, dav, &fresh); err != nil {
		t.Fatal(err)
	}
	if pw, _ := s.ConnectionPassword(ctx, "c2"); pw != "v1.new" {
		t.Errorf("password = %q", pw)
	}
	dav.ID = "nope"
	if err := s.UpdateConnection(ctx, dav, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing update err = %v", err)
	}
	if err := s.DeleteConnection(ctx, "c2"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConnection(ctx, "c2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete err = %v", err)
	}
	if _, err := s.ConnectionPassword(ctx, "c2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ConnectionPassword err = %v", err)
	}
}
