// Package syncengine assembles the engine of a sync task: the protocol
// picks the planner (rclone for SFTP and WebDAV, rsync for rsync over
// SSH), synccore does the rest.
package syncengine

import (
	"context"
	"errors"
	"fmt"

	"tern/internal/engine"
	"tern/internal/engine/rclone"
	"tern/internal/engine/rsync"
	"tern/internal/engine/synccore"
	"tern/internal/model"
	"tern/internal/mounts"
	"tern/internal/store"
)

// Deps is what the sync engines need from the rest of the process.
type Deps struct {
	Access              engine.AccessOpener
	Store               *store.Store
	RcloneBin, RsyncBin string // defaults "rclone" and "rsync"
}

func configErr(format string, args ...any) error {
	return &model.KindError{Kind: model.KindConfig, Msg: fmt.Sprintf(format, args...)}
}

// NewBuilder returns the engine builder for sync tasks. It makes sync and
// dry-run runs.
func NewBuilder(d Deps) engine.Builder {
	return func(t model.Task, req engine.Request) (engine.Engine, error) {
		if t.Type != model.TaskSync || t.Sync == nil {
			return nil, configErr("task %q is not a sync task", t.Name)
		}
		if req.Kind != model.RunSync && req.Kind != model.RunDryRun {
			return nil, configErr("a sync task cannot do a %q run", req.Kind)
		}
		connID := t.Sync.ConnectionID
		conn, err := d.Store.GetConnection(context.Background(), connID)
		if err != nil {
			return nil, &model.KindError{Kind: model.KindConfig, Msg: "the connection of the task is not available", Err: err}
		}
		var p synccore.Planner
		switch t.Sync.Protocol {
		case "sftp", "webdav":
			p = rclone.NewPlanner(t, conn, rclone.Deps{Access: d.Access, Bin: d.RcloneBin})
		case "rsync":
			p = rsync.NewPlanner(t, conn, rsync.Deps{Access: d.Access, Bin: d.RsyncBin})
		default:
			return nil, configErr("unknown sync protocol %q", t.Sync.Protocol)
		}
		check := func(ctx context.Context) error {
			err := d.Access.Check(ctx, connID)
			if errors.Is(err, store.ErrNotFound) {
				return &model.KindError{Kind: model.KindConfig, Msg: "the connection of the task no longer exists", Err: err}
			}
			return err
		}
		return synccore.New(t, req, p, check, mounts.IsMounted), nil
	}
}
