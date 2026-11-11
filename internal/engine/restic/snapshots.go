package restic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"tern/internal/model"
	"tern/internal/proc"
	"tern/internal/redact"
)

// Snapshot is one snapshot in a repository.
type Snapshot struct {
	ID, ShortID string
	Time        time.Time
	Host        string
	Paths, Tags []string
	Mine        bool // made by the task asked about: carries its tern-<task ID> tag
}

// maxSnapshotJSON bounds what a snapshot listing may hold in memory.
const maxSnapshotJSON = 64 << 20

type capped struct {
	buf bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.max {
		return 0, errors.New("restic: snapshot listing is too large")
	}
	return c.buf.Write(p)
}

// ListSnapshots lists every snapshot in the repository, newest first. The
// repository may hold snapshots of other tasks and other machines; Mine
// tells this task's apart. The password file must already be in iv.Dir.
func ListSnapshots(ctx context.Context, iv Invocation, taskID string) ([]Snapshot, error) {
	out := &capped{max: maxSnapshotJSON}
	cmd := iv.Cmd("snapshots", "--json")
	cmd.Stdout = out // one long line; the line reader would cut it
	res, err := proc.Run(ctx, cmd)
	if err != nil {
		return nil, &model.KindError{Kind: model.KindConfig, Msg: "cannot start restic", Err: err}
	}
	if res.Signaled {
		return nil, &model.KindError{Kind: model.KindUnknown, Msg: "restic snapshots was stopped", Err: context.Cause(ctx)}
	}
	if res.ExitCode != exitOK {
		return nil, exitError(iv.RepoPath, "snapshots", res, redact.New())
	}
	var raw []struct {
		ID       string    `json:"id"`
		ShortID  string    `json:"short_id"`
		Time     time.Time `json:"time"`
		Hostname string    `json:"hostname"`
		Paths    []string  `json:"paths"`
		Tags     []string  `json:"tags"`
	}
	if err := json.Unmarshal(out.buf.Bytes(), &raw); err != nil {
		return nil, fmt.Errorf("restic: unreadable snapshot listing: %w", err)
	}
	tag := Tag(taskID)
	snaps := make([]Snapshot, 0, len(raw))
	for _, r := range raw {
		s := Snapshot{ID: r.ID, ShortID: r.ShortID, Time: r.Time.UTC(), Host: r.Hostname, Paths: r.Paths, Tags: r.Tags}
		if s.Paths == nil {
			s.Paths = []string{}
		}
		if s.Tags == nil {
			s.Tags = []string{}
		}
		for _, t := range s.Tags {
			if t == tag {
				s.Mine = true
			}
		}
		snaps = append(snaps, s)
	}
	sort.SliceStable(snaps, func(i, j int) bool { return snaps[i].Time.After(snaps[j].Time) })
	return snaps, nil
}
