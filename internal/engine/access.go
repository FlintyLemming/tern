package engine

import (
	"context"

	"tern/internal/access"
	"tern/internal/redact"
	"tern/internal/rundir"
)

// AccessOpener is how engines get at a connection. access.Opener is the
// real one; tests substitute their own.
type AccessOpener interface {
	// Open prepares connID for child processes of one run; see access.Opener.
	Open(ctx context.Context, connID string, dir *rundir.Dir, red *redact.Redactor) (access.Access, func(), error)
	// Check is the connectivity and authentication preflight.
	Check(ctx context.Context, connID string) error
}

var _ AccessOpener = access.Opener{}
