package restic

import (
	"tern/internal/engine"
	"tern/internal/model"
)

// restic's exit codes, stable since 0.17.
const (
	exitOK           = 0
	exitIncomplete   = 3  // backup finished, some source files could not be read
	exitNoRepository = 10 // repository does not exist
	exitLocked       = 11 // repository is locked
	exitWrongPass    = 12 // wrong repository password
)

// ExitKind sorts a finished restic call. warning is true for a backup that
// completed without some unreadable files; kind is "" when the call did
// not fail. A locked repository is KindUnknown, hence retried: the lock is
// stale (Cleanup removes it) or somebody else's, who will be done later.
func ExitKind(code int, stderrTail string) (kind model.ErrKind, warning bool) {
	switch code {
	case exitOK:
		return "", false
	case exitIncomplete:
		return "", true
	case exitNoRepository:
		return model.KindConfig, false
	case exitLocked:
		return model.KindUnknown, false
	case exitWrongPass:
		return model.KindAuth, false
	}
	if kind, ok := engine.ClassifyStderr(stderrTail); ok {
		return kind, false
	}
	return model.KindUnknown, false
}
