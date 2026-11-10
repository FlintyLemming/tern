//go:build linux

package app

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestStartRejectsRuntimeDirOnDisk(t *testing.T) {
	cfg := testCfg(t)
	disk, err := os.MkdirTemp("/var/tmp", "tern")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(disk)
	cfg.RuntimeDir = disk
	a, err := Start(context.Background(), cfg, "test")
	if err == nil {
		a.Shutdown(context.Background())
		t.Fatal("Start accepted a runtime directory on disk")
	}
	if !strings.Contains(err.Error(), "tmpfs") {
		t.Errorf("err = %v", err)
	}
	// Nothing is left open: the same data directory starts fine afterwards.
	cfg.RuntimeDir = runtimeDir(t)
	start(t, cfg)
}
