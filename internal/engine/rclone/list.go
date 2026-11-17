package rclone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"tern/internal/model"
	"tern/internal/proc"
	"tern/internal/redact"
)

// maxListJSON bounds what one folder listing may hold in memory.
const maxListJSON = 32 << 20

type capped struct {
	buf bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.max {
		return 0, errors.New("rclone: folder listing is too large")
	}
	return c.buf.Write(p)
}

// ListDirs lists the folders directly inside dir on the remote of the
// rclone.conf at conf. dir "" is the remote's starting point: the SSH
// user's home directory or the WebDAV address. found is false when rclone
// reports that dir does not exist. Other failures are *model.KindError
// with the reason redacted by red.
func ListDirs(ctx context.Context, bin, conf, dir string, red *redact.Redactor) (names []string, found bool, err error) {
	if bin == "" {
		bin = "rclone"
	}
	out := &capped{max: maxListJSON}
	args := []string{"lsjson", remote(dir), "--config", conf, "--dirs-only", "--no-modtime", "--no-mimetype",
		"--timeout", "30s", "--contimeout", "15s", "--low-level-retries", "1"}
	res, err := proc.Run(ctx, proc.Cmd{Path: bin, Args: args, Stdout: out})
	if err != nil {
		return nil, false, &model.KindError{Kind: model.KindConfig, Msg: "cannot start rclone", Err: err}
	}
	if res.Signaled {
		return nil, false, &model.KindError{Kind: model.KindNetwork, Msg: "listing the remote folder took too long", Err: context.Cause(ctx)}
	}
	switch res.ExitCode {
	case exitOK:
	case exitDirNotFound:
		return nil, false, nil
	default:
		msg := fmt.Sprintf("rclone lsjson failed (exit %d)", res.ExitCode)
		if r := reason(red, res.StderrTail); r != "" {
			msg += ": " + r
		}
		return nil, false, &model.KindError{Kind: ExitKind(res.ExitCode, res.StderrTail), Msg: msg}
	}
	var entries []struct {
		Name  string
		IsDir bool
	}
	if err := json.Unmarshal(out.buf.Bytes(), &entries); err != nil {
		return nil, false, fmt.Errorf("rclone: unreadable folder listing: %w", err)
	}
	for _, e := range entries {
		if e.IsDir && e.Name != "" {
			names = append(names, e.Name)
		}
	}
	return names, true, nil
}
