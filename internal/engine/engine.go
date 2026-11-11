// Package engine is the seam between the run pipeline and the tools that
// move data. The pipeline only knows this interface; restic, rclone and
// rsync plug in behind it.
package engine

import (
	"context"
	"io"

	"tern/internal/model"
	"tern/internal/redact"
	"tern/internal/rundir"
)

// Env is what the pipeline lends an engine for one run.
type Env struct {
	Run      model.Run
	Dir      *rundir.Dir      // tmpfs scratch space, removed after the run
	Log      io.Writer        // the run log; already goes through Redact
	Redact   *redact.Redactor // register every secret that could show up in output
	Progress func(model.Progress)
}

// Request says what kind of run to build an engine for.
type Request struct {
	Kind         model.RunKind
	AllowDeletes bool // the user lifted the mirror delete limit for this one run
	Params       map[string]string
}

// Engine does the work of one run. If it also implements io.Closer, Close
// is called once when the run is over.
type Engine interface {
	// Units names the units of work of this run, in execution order.
	Units() []string
	// Preflight checks connectivity, authentication and paths before each
	// attempt. Failures are *model.KindError.
	Preflight(ctx context.Context, env Env) error
	// RunUnit runs unit i. A non-nil error means the unit failed; otherwise
	// the result's Status is success or warning.
	RunUnit(ctx context.Context, env Env, i int) (model.UnitResult, error)
	// Cleanup is called before every retry.
	Cleanup(ctx context.Context, env Env) error
	// Describe returns the equivalent command lines, with placeholders
	// where credentials would be.
	Describe() []string
}
