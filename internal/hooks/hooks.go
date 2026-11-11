// Package hooks runs the pre- and post-task commands: shell script text,
// either inside the container or on the far end of an SSH connection.
package hooks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"tern/internal/access"
	"tern/internal/model"
	"tern/internal/proc"
	"tern/internal/rundir"
	"tern/internal/sshx"
)

// Vars is what a hook learns about its task through TERN_ variables.
// Credentials are never among them.
type Vars struct {
	TaskID, TaskName, TaskType, Trigger string
	Result                              *ResultVars // post-task command only
}

type ResultVars struct {
	Status                    string
	DurationSec, Bytes, Files int64
	Error                     string
}

// Runner executes hooks.
type Runner struct {
	Access     access.Opener // for hooks that run over SSH
	RuntimeDir string        // tmpfs base for the ssh material
	SSHBin     string        // default "ssh"
}

// Environ renders v as NAME=value pairs.
func Environ(v Vars) []string {
	env := []string{
		"TERN_TASK_ID=" + v.TaskID,
		"TERN_TASK_NAME=" + v.TaskName,
		"TERN_TASK_TYPE=" + v.TaskType,
		"TERN_TRIGGER=" + v.Trigger,
	}
	if r := v.Result; r != nil {
		env = append(env,
			"TERN_RESULT="+r.Status,
			"TERN_DURATION_SECONDS="+strconv.FormatInt(r.DurationSec, 10),
			"TERN_BYTES="+strconv.FormatInt(r.Bytes, 10),
			"TERN_FILES="+strconv.FormatInt(r.Files, 10),
			"TERN_ERROR="+r.Error,
		)
	}
	return env
}

// shellQuote wraps s in single quotes for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// lineLog writes lines from two goroutines to one writer.
type lineLog struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lineLog) line(p []byte) {
	if l.w == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.w.Write(append(append(make([]byte, 0, len(p)+1), p...), '\n'))
}

// Run executes h and copies its output to log. An empty script is a no-op.
// The error of a failed hook mentions "exit <code>" or "timeout".
func (r Runner) Run(ctx context.Context, h model.Hook, v Vars, log io.Writer) error {
	if strings.TrimSpace(h.Script) == "" {
		return nil
	}
	timeout := time.Duration(h.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := &lineLog{w: log}
	cmd := proc.Cmd{OnStdout: out.line, OnStderr: out.line}
	switch h.Where {
	case model.HookSSH:
		// The remote sshd decides which variables it accepts, so they travel
		// as the first lines of the script instead.
		var stdin strings.Builder
		for _, kv := range Environ(v) {
			name, value, _ := strings.Cut(kv, "=")
			stdin.WriteString("export " + name + "=" + shellQuote(value) + "\n")
		}
		stdin.WriteString(h.Script)

		dir, err := rundir.New(r.RuntimeDir, model.NewID())
		if err != nil {
			return fmt.Errorf("hook: %w", err)
		}
		defer dir.Close()
		acc, release, err := r.Access.Open(ctx, h.ConnectionID, dir, nil)
		if err != nil {
			return fmt.Errorf("hook: open connection: %w", err)
		}
		defer release()
		if acc.Kind != model.ConnSSH {
			return errors.New("hook: the connection is not an SSH connection")
		}
		cmd.Path = r.SSHBin
		if cmd.Path == "" {
			cmd.Path = "ssh"
		}
		cmd.Args = []string{"-F", acc.SSHConfig, sshx.Alias, "sh", "-s"}
		cmd.Stdin = strings.NewReader(stdin.String())
	default:
		cmd.Path = "/bin/sh"
		cmd.Args = []string{"-s"}
		cmd.Env = Environ(v)
		cmd.Stdin = strings.NewReader(h.Script)
	}

	res, err := proc.Run(ctx, cmd)
	switch {
	case err != nil:
		return fmt.Errorf("hook: start: %w", err)
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("hook: timeout after %s", timeout)
	case ctx.Err() != nil:
		return fmt.Errorf("hook: canceled: %w", context.Cause(ctx))
	case res.ExitCode != 0:
		return fmt.Errorf("hook: exit %d", res.ExitCode)
	}
	return nil
}
