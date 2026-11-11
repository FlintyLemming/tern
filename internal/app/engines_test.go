package app

import (
	"context"
	"strings"
	"testing"

	"tern/internal/config"
	"tern/internal/engine"
	"tern/internal/model"
	"tern/internal/store"
	"tern/internal/vault"
)

// Backup tasks get the restic engine. Without a connection the builder
// cannot go far, but it is restic's builder that says so.
func TestBackupEngineRegistered(t *testing.T) {
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reg := newRegistry(config.Config{DataDir: t.TempDir()}, st, vault.New(st, t.TempDir()))
	task := model.Task{Type: model.TaskBackup, Name: "docs", Backup: &model.BackupSpec{ConnectionID: "missing"}}
	_, err = reg.Build(task, engine.Request{Kind: model.RunBackup})
	if model.KindOf(err) != model.KindConfig || strings.Contains(err.Error(), "no engine") || !strings.Contains(err.Error(), "connection") {
		t.Errorf("Build = %v, want the restic builder's complaint about the connection", err)
	}
}
