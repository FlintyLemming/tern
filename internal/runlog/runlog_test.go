package runlog

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogFileMode(t *testing.T) {
	dir := t.TempDir()
	w, err := Create(dir, "run1")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("hello\n"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "logs", "run1.log"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	if _, err := Create(dir, "../escape"); err == nil {
		t.Error("Create accepted a run ID with a path separator")
	}
}

func TestLongLineTruncated(t *testing.T) {
	dir := t.TempDir()
	w, _ := Create(dir, "r")
	// Fed in pieces, as it would arrive from a pipe.
	for i := 0; i < 200; i++ {
		w.Write(bytes.Repeat([]byte("x"), 1024))
	}
	w.Write([]byte("\nnext\n"))
	w.Close()
	data, _ := os.ReadFile(filepath.Join(dir, "logs", "r.log"))
	lines := strings.Split(string(data), "\n")
	if len(lines) != 3 || lines[1] != "next" || lines[2] != "" {
		t.Fatalf("got %d lines, second %q", len(lines), lines[min(1, len(lines)-1)])
	}
	if len(lines[0]) > MaxLine || len(lines[0]) < MaxLine-16 {
		t.Errorf("first line is %d bytes, want about %d", len(lines[0]), MaxLine)
	}
}

func TestLogCap(t *testing.T) {
	dir := t.TempDir()
	w, _ := Create(dir, "r")
	line := append(bytes.Repeat([]byte("y"), 1023), '\n')
	chunk := bytes.Repeat(line, 1024) // 1 MiB
	for i := 0; i < 70; i++ {
		if n, err := w.Write(chunk); err != nil || n != len(chunk) {
			t.Fatalf("Write %d = %d, %v", i, n, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "logs", "r.log"))
	const notice = "[log truncated]\n"
	if len(data) > MaxSize+len(notice) {
		t.Errorf("file is %d bytes, cap is %d", len(data), MaxSize)
	}
	if len(data) < MaxSize-2048 {
		t.Errorf("file is only %d bytes", len(data))
	}
	if !bytes.HasSuffix(data, []byte(notice)) || bytes.Count(data, []byte(notice)) != 1 {
		t.Errorf("want exactly one trailing truncation notice; tail %q", data[len(data)-40:])
	}
}

func TestReadPaging(t *testing.T) {
	dir := t.TempDir()
	w, _ := Create(dir, "r")
	var want []byte
	for i := 0; i < 500; i++ {
		line := []byte(strings.Repeat("z", i%97) + "\n")
		want = append(want, line...)
		w.Write(line)
	}
	w.Close()

	var got []byte
	var offset int64
	for {
		data, next, err := Read(dir, "r", offset, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if next < offset || len(data) > 1000 || next != offset+int64(len(data)) {
			t.Fatalf("offset %d → next %d with %d bytes", offset, next, len(data))
		}
		if len(data) == 0 {
			break
		}
		got = append(got, data...)
		offset = next
	}
	if !bytes.Equal(got, want) {
		t.Errorf("paged read differs from the file (%d vs %d bytes)", len(got), len(want))
	}
	if data, next, err := Read(dir, "missing", 0, 100); err != nil || len(data) != 0 || next != 0 {
		t.Errorf("Read of a missing log = %q, %d, %v; want empty", data, next, err)
	}
	if data, next, err := Read(dir, "r", int64(len(want))+50, 100); err != nil || len(data) != 0 || next != int64(len(want)) {
		t.Errorf("Read past the end = %q, %d, %v", data, next, err)
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"a", "b"} {
		w, _ := Create(dir, id)
		w.Write([]byte("x\n"))
		w.Close()
	}
	if err := Remove(dir, []string{"a", "never-existed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "logs", "a.log")); !os.IsNotExist(err) {
		t.Errorf("a.log still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "logs", "b.log")); err != nil {
		t.Errorf("b.log gone: %v", err)
	}
}
