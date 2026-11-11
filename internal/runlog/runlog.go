// Package runlog stores the log of each run as a plain file under
// <dataDir>/logs. Lines and the file as a whole are capped, so a tool that
// floods its output cannot fill the data volume.
package runlog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	MaxLine = 64 << 10 // bytes of one line that are kept
	MaxSize = 64 << 20 // bytes of one run's log
	notice  = "[log truncated]\n"
)

func path(dataDir, runID string) (string, error) {
	if runID == "" || runID == "." || runID == ".." || strings.ContainsAny(runID, "/\\\x00") {
		return "", fmt.Errorf("runlog: %q is not a run ID", runID)
	}
	return filepath.Join(dataDir, "logs", runID+".log"), nil
}

// Writer appends to one run's log. It is safe for concurrent use.
type Writer struct {
	mu      sync.Mutex
	f       *os.File
	size    int  // bytes written so far
	lineLen int  // bytes of the current, unfinished line already written
	full    bool // the cap was reached; everything else is dropped
}

// Create opens <dataDir>/logs/<runID>.log for writing, mode 0600.
func Create(dataDir, runID string) (*Writer, error) {
	p, err := path(dataDir, runID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil { // OpenFile is subject to umask and keeps an existing mode
		f.Close()
		return nil, err
	}
	return &Writer{f: f}, nil
}

// Write always reports p as fully written: output that exceeds a cap is
// dropped on purpose, and the producer must not stall or fail because of it.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.full || w.f == nil {
		return len(p), nil
	}
	out := make([]byte, 0, len(p))
	lineLen := w.lineLen
	for rest := p; len(rest) > 0; {
		i := bytes.IndexByte(rest, '\n')
		seg := rest
		if i >= 0 {
			seg = rest[:i]
		}
		if room := MaxLine - lineLen; len(seg) > room {
			seg = seg[:max(room, 0)]
		}
		out = append(out, seg...)
		lineLen += len(seg)
		if i < 0 {
			break
		}
		out = append(out, '\n')
		lineLen = 0
		rest = rest[i+1:]
	}
	// One byte is held back so a line cut by the cap can still be terminated.
	if room := MaxSize - 1 - w.size; len(out) > room {
		out = out[:max(room, 0)]
		if n := len(out); (n > 0 && out[n-1] != '\n') || (n == 0 && w.lineLen > 0) {
			out = append(out, '\n')
		}
		out = append(out, notice...)
		w.full = true
	}
	n, err := w.f.Write(out)
	w.size += n
	w.lineLen = lineLen
	if err != nil {
		return len(p), err
	}
	return len(p), nil
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// Read returns up to max bytes of the log starting at offset, and the offset
// to continue from. A run without a log file reads as empty.
func Read(dataDir, runID string, offset int64, max int) (data []byte, next int64, err error) {
	p, err := path(dataDir, runID)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	offset = min(max64(offset, 0), fi.Size())
	buf := make([]byte, min(int64(max), fi.Size()-offset))
	n, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return nil, offset, err
	}
	return buf[:n], offset + int64(n), nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// Remove deletes the logs of the given runs; missing files are fine.
func Remove(dataDir string, runIDs []string) error {
	var errs []error
	for _, id := range runIDs {
		p, err := path(dataDir, id)
		if err == nil {
			err = os.Remove(p)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
