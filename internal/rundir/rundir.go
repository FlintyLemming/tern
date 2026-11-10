// Package rundir manages per-run scratch directories on tmpfs. Anything a
// child process needs as a file (sockets, generated configs) lives here and
// nowhere else, so it never reaches a disk.
package rundir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Dir is one run's private directory.
type Dir struct{ Path string }

func checkName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return fmt.Errorf("rundir: %q is not a plain file name", name)
	}
	return nil
}

// Sweep empties base: whatever is in there was left by a process that is gone.
func Sweep(base string) error {
	entries, err := os.ReadDir(base)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		errs = append(errs, os.RemoveAll(filepath.Join(base, e.Name())))
	}
	return errors.Join(errs...)
}

// New creates <base>/<runID> with mode 0700. The directory must not exist yet.
func New(base, runID string) (*Dir, error) {
	if err := checkName(runID); err != nil {
		return nil, err
	}
	path := filepath.Join(base, runID)
	if err := os.Mkdir(path, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o700); err != nil { // Mkdir is subject to umask
		os.Remove(path)
		return nil, err
	}
	return &Dir{Path: path}, nil
}

// WriteSecret creates <dir>/<name> with mode 0600 holding data. It refuses to
// overwrite and to leave the directory.
func (d *Dir) WriteSecret(name string, data []byte) (path string, err error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	path = filepath.Join(d.Path, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// Close deletes the directory and everything in it.
func (d *Dir) Close() error { return os.RemoveAll(d.Path) }
