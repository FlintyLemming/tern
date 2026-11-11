package restic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"tern/internal/engine"
	"tern/internal/model"
	"tern/internal/proc"
	"tern/internal/store"
	"tern/internal/vault"
)

// Deps is what the restic engines need from the rest of the process.
type Deps struct {
	Access  engine.AccessOpener
	Store   *store.Store
	Vault   *vault.Vault
	DataDir string
	Bin     string // default "restic"
}

// PasswordAAD binds a sealed repository password to its task.
func PasswordAAD(taskID string) string { return "tasks/" + taskID + "/repo_password" }

// Tag marks the snapshots a task made. The retention policy only ever
// touches snapshots carrying it.
func Tag(taskID string) string { return "tern-" + taskID }

func configErr(format string, args ...any) error {
	return &model.KindError{Kind: model.KindConfig, Msg: fmt.Sprintf(format, args...)}
}

// NewBuilder returns the engine builder for backup tasks. The kind of run
// selects the engine: backup, prune, check or restore.
func NewBuilder(d Deps) engine.Builder {
	return func(t model.Task, req engine.Request) (engine.Engine, error) {
		if t.Type != model.TaskBackup || t.Backup == nil {
			return nil, configErr("task %q is not a backup task", t.Name)
		}
		conn, err := d.Store.GetConnection(context.Background(), t.Backup.ConnectionID)
		if err != nil {
			return nil, &model.KindError{Kind: model.KindConfig, Msg: "the connection of the task is not available", Err: err}
		}
		s := &session{d: d, task: t, conn: conn}
		switch req.Kind {
		case model.RunBackup:
			return &backupEngine{s}, nil
		}
		return nil, configErr("a backup task cannot do a %q run", req.Kind)
	}
}

// session is what the engines of one run share: the task, and the opened
// access to the repository. Access is opened once per run and reused by
// every retry.
type session struct {
	d    Deps
	task model.Task
	conn model.Connection

	mu      sync.Mutex
	iv      Invocation
	opened  bool
	release func()
}

func (s *session) spec() *model.BackupSpec { return s.task.Backup }

// check is the connectivity and authentication preflight.
func (s *session) check(ctx context.Context) error {
	err := s.d.Access.Check(ctx, s.spec().ConnectionID)
	if errors.Is(err, store.ErrNotFound) {
		return &model.KindError{Kind: model.KindConfig, Msg: "the connection of the task no longer exists", Err: err}
	}
	return err
}

// ensureOpen prepares the run directory for restic calls: SSH material,
// and the repository password as a file. The password is registered with
// the redactor before anything can print it.
func (s *session) ensureOpen(ctx context.Context, env engine.Env) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opened {
		return nil
	}
	sealed, err := s.d.Store.TaskRepoPassword(ctx, s.task.ID)
	if err != nil {
		return &model.KindError{Kind: model.KindConfig, Msg: "the repository password of the task cannot be read", Err: err}
	}
	if sealed == "" {
		return configErr("the task has no repository password")
	}
	pw, err := s.d.Vault.Open(PasswordAAD(s.task.ID), sealed)
	if err != nil {
		return &model.KindError{Kind: model.KindConfig, Msg: "the repository password of the task cannot be decrypted", Err: err}
	}
	defer clear(pw)
	env.Redact.Add(pw)

	acc, release, err := s.d.Access.Open(ctx, s.spec().ConnectionID, env.Dir, env.Redact)
	if err != nil {
		return err
	}
	iv := Invocation{Bin: s.d.Bin, SSHConfig: acc.SSHConfig, Dir: env.Dir, DataDir: s.d.DataDir,
		RepoPath: s.spec().RepoPath, Connections: s.spec().Connections}
	if err := iv.WritePassword(pw); err != nil {
		release()
		return fmt.Errorf("restic: write the password file: %w", err)
	}
	s.iv, s.release, s.opened = iv, release, true
	return nil
}

// Close releases the access material. It makes every engine an io.Closer.
func (s *session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release != nil {
		s.release()
		s.release = nil
	}
	return nil
}

func logLine(env engine.Env) func([]byte) {
	return func(line []byte) { fmt.Fprintf(env.Log, "%s\n", line) }
}

// exec runs one restic call. stderr always goes to the run log; stdout
// goes to onStdout. The error says the call could not be made or was
// stopped; how it exited is in the result.
func (s *session) exec(ctx context.Context, env engine.Env, onStdout func([]byte), args ...string) (proc.Result, error) {
	fmt.Fprintf(env.Log, "$ restic %s\n", strings.Join(args, " "))
	cmd := s.iv.Cmd(args...)
	cmd.OnStdout = onStdout
	cmd.OnStderr = logLine(env)
	res, err := proc.Run(ctx, cmd)
	if err != nil {
		return res, &model.KindError{Kind: model.KindConfig, Msg: "cannot start restic", Err: err}
	}
	if res.Signaled {
		return res, &model.KindError{Kind: model.KindUnknown, Msg: "restic " + args[0] + " was stopped", Err: context.Cause(ctx)}
	}
	return res, nil
}

// lastLine is the last thing restic said on stderr, as a one-line reason.
// With --json a fatal error arrives as a JSON object.
func lastLine(env engine.Env, tail string) string {
	lines := strings.Split(strings.TrimSpace(tail), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	var msg struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(line), &msg) == nil && msg.Message != "" {
		line = msg.Message
	}
	if len(line) > 300 {
		line = line[:300]
	}
	// It ends up in the run record, which is not behind the log's redactor.
	var buf bytes.Buffer
	w := env.Redact.Writer(&buf)
	fmt.Fprintln(w, line)
	w.Close()
	return strings.TrimSpace(buf.String())
}

// failure turns a non-zero exit of `restic <sub>` into an error.
func (s *session) failure(env engine.Env, sub string, res proc.Result) error {
	kind, _ := ExitKind(res.ExitCode, res.StderrTail)
	var msg string
	switch res.ExitCode {
	case exitNoRepository:
		msg = "there is no restic repository at " + s.spec().RepoPath + " on the remote end"
	case exitLocked:
		msg = "the repository is locked by another restic process"
	case exitWrongPass:
		msg = "wrong repository password"
	default:
		msg = fmt.Sprintf("restic %s failed (exit %d)", sub, res.ExitCode)
		if reason := lastLine(env, res.StderrTail); reason != "" {
			msg += ": " + reason
		}
	}
	return &model.KindError{Kind: kind, Msg: msg}
}

// call runs `restic <args>` with its output in the run log and returns an
// error unless it exits 0.
func (s *session) call(ctx context.Context, env engine.Env, args ...string) error {
	res, err := s.exec(ctx, env, logLine(env), args...)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return s.failure(env, args[0], res)
	}
	return nil
}

// unlock removes stale locks: those of processes that are gone. A lock
// somebody still holds stays, so --remove-all must never appear here.
func (s *session) unlock(ctx context.Context, env engine.Env) error {
	return s.call(ctx, env, "unlock")
}

// Cleanup runs before a retry: the attempt that just failed may have left
// its lock behind.
func (s *session) Cleanup(ctx context.Context, env engine.Env) error {
	s.mu.Lock()
	opened := s.opened
	s.mu.Unlock()
	if !opened {
		return nil // never got as far as the repository
	}
	return s.unlock(ctx, env)
}
