package mounts

import (
	"errors"
	"syscall"
)

func isNotDir(err error) bool { return errors.Is(err, syscall.ENOTDIR) }
