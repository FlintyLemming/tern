package vault

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

type keyfileStatus int

const (
	keyfileOK     keyfileStatus = iota
	keyfileAbsent               // nothing at the path
	keyfileBad                  // something is there but it is not a readable 32-byte file
)

// readKeyfile reads the master key without ever writing anything.
func readKeyfile(path string) ([]byte, keyfileStatus) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, keyfileAbsent
	}
	if err != nil || !fi.Mode().IsRegular() {
		return nil, keyfileBad
	}
	key, err := os.ReadFile(path)
	if err != nil || len(key) != keyLen {
		return nil, keyfileBad
	}
	return key, keyfileOK
}

// ensureKeyfile returns the master key, creating the file when nothing is
// there. Anything already at the path is used as is or refused, never replaced.
func ensureKeyfile(path string) ([]byte, error) {
	key, st := readKeyfile(path)
	switch st {
	case keyfileOK:
		return key, nil
	case keyfileBad:
		return nil, fmt.Errorf("vault: %s exists but is not a readable %d-byte key file; refusing to replace it", path, keyLen)
	}
	key = make([]byte, keyLen)
	rand.Read(key)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("vault: create key file: %w", err)
	}
	_, err = f.Write(key)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path) // we created it just now, and it is incomplete
		return nil, fmt.Errorf("vault: write key file: %w", err)
	}
	return key, nil
}
