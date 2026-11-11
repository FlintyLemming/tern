package restic

import (
	"context"
	"fmt"

	"tern/internal/engine"
	"tern/internal/model"
)

// maintEngine is a prune or check run. Both are slow on a weak link, so
// they are runs of their own, on their own periods, and a failure of
// either says nothing about the backups themselves.
type maintEngine struct {
	*session
	unit string // "prune" | "check"
}

func (e *maintEngine) Units() []string { return []string{e.unit} }

func (e *maintEngine) Preflight(ctx context.Context, env engine.Env) error {
	if err := e.check(ctx); err != nil {
		return err
	}
	return e.ensureOpen(ctx, env)
}

func (e *maintEngine) RunUnit(ctx context.Context, env engine.Env, _ int) (model.UnitResult, error) {
	out := model.UnitResult{Name: e.unit, Status: model.StatusSuccess}
	if e.unit == "prune" {
		// prune needs the exclusive lock; a stale one would block it.
		if err := e.unlock(ctx, env); err != nil {
			if ctx.Err() != nil {
				return out, err
			}
			fmt.Fprintf(env.Log, "removing stale locks failed, going on: %v\n", err)
		}
	}
	return out, e.call(ctx, env, e.unit)
}

func (e *maintEngine) Describe() []string {
	return append(e.exportLines(), "restic "+e.unit)
}
