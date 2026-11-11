package sysinfo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// procFixture builds <root>/sys/net/ipv4 with the two sysctl files.
func procFixture(t *testing.T, available, current string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "sys", "net", "ipv4")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "tcp_available_congestion_control"), []byte(available), 0o644)
	os.WriteFile(filepath.Join(dir, "tcp_congestion_control"), []byte(current), 0o644)
	return root
}

func TestReadBBR(t *testing.T) {
	b := ReadBBR(procFixture(t, "reno cubic bbr\n", "bbr\n"))
	if !b.Supported || !b.Active || len(b.Available) != 3 || b.Current != "bbr" {
		t.Errorf("BBR = %+v", b)
	}
}

func TestReadBBRSupportedNotActive(t *testing.T) {
	b := ReadBBR(procFixture(t, "reno cubic bbr\n", "cubic\n"))
	if !b.Supported || b.Active || b.Current != "cubic" {
		t.Errorf("BBR = %+v", b)
	}
}

func TestReadBBRUnsupported(t *testing.T) {
	b := ReadBBR(procFixture(t, "reno cubic", "cubic"))
	if b.Supported || b.Active {
		t.Errorf("BBR = %+v", b)
	}
}

func TestReadBBRMissingFiles(t *testing.T) {
	b := ReadBBR(t.TempDir())
	if b.Supported || b.Active || b.Current != "" || len(b.Available) != 0 {
		t.Errorf("BBR = %+v", b)
	}
}

func TestToolsMissingBinary(t *testing.T) {
	resetTools()
	t.Setenv("PATH", t.TempDir())
	tools := Tools(context.Background())
	if len(tools) != 4 {
		t.Fatalf("Tools = %+v", tools)
	}
	for i, want := range []string{"restic", "rclone", "rsync", "ssh"} {
		if tools[i].Name != want || tools[i].Found || tools[i].Version != "" {
			t.Errorf("tool %d = %+v", i, tools[i])
		}
	}
}

func TestToolsVersionsAndCache(t *testing.T) {
	resetTools()
	dir := t.TempDir()
	script := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script("restic", `[ "$1" = version ] && echo "restic 0.19.1 compiled with go1.26 on linux/arm64"`)
	script("rclone", `[ "$1" = version ] && printf 'rclone v1.76.0\n- os/version: alpine\n'`)
	script("rsync", `[ "$1" = --version ] && echo "rsync  version 3.4.1  protocol version 32"`)
	script("ssh", `[ "$1" = -V ] && echo "OpenSSH_10.2p1, OpenSSL 3.5.4" >&2`)
	t.Setenv("PATH", dir)

	want := map[string]string{"restic": "0.19.1", "rclone": "1.76.0", "rsync": "3.4.1", "ssh": "10.2p1"}
	for _, tool := range Tools(context.Background()) {
		if !tool.Found || tool.Version != want[tool.Name] {
			t.Errorf("%+v, want version %q", tool, want[tool.Name])
		}
	}
	// Cached for the life of the process: the image does not change.
	t.Setenv("PATH", t.TempDir())
	for _, tool := range Tools(context.Background()) {
		if !tool.Found {
			t.Errorf("second call looked again: %+v", tool)
		}
	}
}
