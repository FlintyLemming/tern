package store

import "errors"

// Returned by Open.
var (
	ErrLockHeld    = errors.New("store: data directory is in use by another tern process")
	ErrNewerSchema = errors.New("store: database was written by a newer tern version")
)

// Returned by the per-table readers and writers.
var (
	ErrNotFound  = errors.New("store: not found")
	ErrVersion   = errors.New("store: version conflict")
	ErrInUse     = errors.New("store: in use")
	ErrDuplicate = errors.New("store: duplicate")
)
