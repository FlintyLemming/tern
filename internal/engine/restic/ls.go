package restic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"tern/internal/model"
	"tern/internal/proc"
	"tern/internal/redact"
)

// ErrNoSnapshot is ListDir's answer for a snapshot ID the repository does
// not have.
var ErrNoSnapshot = errors.New("restic: no such snapshot")

// ValidSnapshotID reports whether id is a full or abbreviated snapshot ID,
// the only form tern passes to restic.
func ValidSnapshotID(id string) bool { return snapshotIDRE.MatchString(id) }

// ListDir lists the folders directly inside dir, an absolute clean path, of
// the snapshot id. found is false when the snapshot has no folder dir. The
// repository is not locked: a listing must work while a prune holds the
// lock. The password file must already be in iv.Dir.
func ListDir(ctx context.Context, iv Invocation, id, dir string) (names []string, found bool, err error) {
	if !ValidSnapshotID(id) || !path.IsAbs(dir) || path.Clean(dir) != dir {
		return nil, false, fmt.Errorf("restic: cannot list %q of snapshot %q", dir, id)
	}
	// `restic ls <id> <dir>` prints the snapshot, the folders leading to
	// dir, dir itself and what is directly inside it, one JSON object a line.
	found = dir == "/"
	cmd := iv.Cmd("ls", "--no-lock", "--json", id, dir)
	cmd.OnStdout = func(line []byte) {
		var n struct {
			Name        string `json:"name"`
			Type        string `json:"type"`
			Path        string `json:"path"`
			StructType  string `json:"struct_type"`
			MessageType string `json:"message_type"`
		}
		if json.Unmarshal(line, &n) != nil || (n.StructType != "node" && n.MessageType != "node") {
			return
		}
		switch {
		case n.Path == dir:
			found = n.Type == "dir"
		case n.Type == "dir" && n.Name != "" && path.Dir(n.Path) == dir:
			names = append(names, n.Name)
		}
	}
	res, err := proc.Run(ctx, cmd)
	if err != nil {
		return nil, false, &model.KindError{Kind: model.KindConfig, Msg: "cannot start restic", Err: err}
	}
	if res.Signaled {
		return nil, false, &model.KindError{Kind: model.KindNetwork, Msg: "listing the snapshot took too long", Err: context.Cause(ctx)}
	}
	if res.ExitCode != exitOK {
		if strings.Contains(res.StderrTail, "no matching ID found") {
			return nil, false, ErrNoSnapshot
		}
		return nil, false, exitError(iv.RepoPath, "ls", res, redact.New())
	}
	if !found {
		return nil, false, nil
	}
	return names, true, nil
}
