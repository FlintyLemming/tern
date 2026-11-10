package model

import "errors"

// ErrKind sorts failures by what should happen next.
type ErrKind string

const (
	KindNetwork ErrKind = "network"  // retried
	KindUnknown ErrKind = "unknown"  // retried: unclassified is treated as recoverable
	KindAuth    ErrKind = "auth"     // none of the following are retried
	KindHostKey ErrKind = "host_key" //
	KindConfig  ErrKind = "config"   //
	KindData    ErrKind = "data"     // individual files failed; rerunning will not help
	KindGuard   ErrKind = "guard"    // tern stopped the run to protect data
)

// Retryable reports whether a failure of this kind is worth another attempt.
func (k ErrKind) Retryable() bool { return k == KindNetwork || k == KindUnknown }

// KindError is an error with a kind and an English, user-facing message.
type KindError struct {
	Kind ErrKind
	Msg  string
	Err  error // underlying cause, may be nil
}

func (e *KindError) Error() string {
	if e.Err == nil {
		return e.Msg
	}
	return e.Msg + ": " + e.Err.Error()
}

func (e *KindError) Unwrap() error { return e.Err }

// KindOf returns "" for nil, the kind of the first *KindError in err's chain,
// and KindUnknown for anything else.
func KindOf(err error) ErrKind {
	if err == nil {
		return ""
	}
	var ke *KindError
	if errors.As(err, &ke) {
		return ke.Kind
	}
	return KindUnknown
}
