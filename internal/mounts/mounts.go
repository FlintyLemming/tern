// Package mounts tells whether a path lies on a volume mounted into the
// container. What is written anywhere else lands in the container's own
// file system and is gone when the container is recreated.
package mounts

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// NearestExisting returns path itself if it exists, otherwise its closest
// ancestor that does. path must be absolute.
func NearestExisting(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("mounts: %q is not an absolute path", path)
	}
	p := filepath.Clean(path)
	for {
		_, err := os.Stat(p)
		if err == nil {
			return p, nil
		}
		// ENOTDIR: an ancestor is a file; keep walking up to it.
		if !errors.Is(err, fs.ErrNotExist) && !isNotDir(err) {
			return "", err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", err
		}
		p = parent
	}
}
