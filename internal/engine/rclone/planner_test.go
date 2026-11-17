package rclone

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"tern/internal/access"
	"tern/internal/engine"
	"tern/internal/engine/faketool"
	"tern/internal/engine/synccore"
	"tern/internal/model"
	"tern/internal/redact"
	"tern/internal/rundir"
)

// fakeAccess hands out SSH access material without a vault or an agent.
type fakeAccess struct {
	mu              sync.Mutex
	opens, releases int
	kind, pass      string
}

func (f *fakeAccess) Open(ctx context.Context, connID string, dir *rundir.Dir, red *redact.Redactor) (access.Access, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	release := func() { f.mu.Lock(); f.releases++; f.mu.Unlock() }
	if f.kind == model.ConnWebDAV {
		red.Add([]byte(f.pass))
		return access.Access{Kind: model.ConnWebDAV, URL: "http://dav.example/", User: "anna", Pass: f.pass}, release, nil
	}
	return access.Access{Kind: model.ConnSSH, SSHConfig: filepath.Join(dir.Path, "ssh_config")}, release, nil
}

func (f *fakeAccess) Check(ctx context.Context, connID string) error { return nil }

type plannerFixture struct {
	t    *testing.T
	acc  *fakeAccess
	tool *faketool.Fake
	log  *bytes.Buffer
	env  engine.Env
	pl   synccore.Planner
}

func newPlanner(t *testing.T, s model.SyncSpec, steps ...faketool.Step) *plannerFixture {
	t.Helper()
	f := &plannerFixture{t: t, acc: &fakeAccess{}, tool: faketool.New(t, "rclone", steps...), log: &bytes.Buffer{}}
	f.env = engine.Env{Dir: newRunDir(t), Log: f.log, Redact: redact.New()}
	task := model.Task{ID: "t1", Type: model.TaskSync, Name: "photos", Sync: &s}
	f.pl = NewPlanner(task, model.Connection{ID: "c1", Type: model.ConnSSH, Host: "nas2.example", Port: 22, Username: "backup"},
		Deps{Access: f.acc, Bin: f.tool.Bin})
	t.Cleanup(func() { f.pl.Close() })
	return f
}

func (f *plannerFixture) prepare() {
	f.t.Helper()
	if err := f.pl.Prepare(context.Background(), f.env); err != nil {
		f.t.Fatal(err)
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPrepareIdempotent(t *testing.T) {
	s := spec("push", "keep")
	s.Excludes = []string{"*.tmp", "/cache/"}
	f := newPlanner(t, s)
	f.prepare()
	f.prepare()
	if f.acc.opens != 1 {
		t.Errorf("Access.Open called %d times", f.acc.opens)
	}
	filter, err := os.ReadFile(filepath.Join(f.env.Dir.Path, "rclone-filter"))
	if err != nil || string(filter) != "- *.tmp\n- /cache/\n" {
		t.Errorf("filter file = %q, %v", filter, err)
	}
	if _, err := os.Stat(filepath.Join(f.env.Dir.Path, "rclone.conf")); err != nil {
		t.Errorf("rclone.conf: %v", err)
	}
}

func TestRemoteState(t *testing.T) {
	pair := model.FolderPair{Local: "/targets/a", Remote: "/srv/a"}
	for _, c := range []struct {
		step faketool.Step
		want synccore.SourceState
		kind model.ErrKind
	}{
		{faketool.Step{Exit: 3, Stderr: "ERROR : error listing: directory not found\n"}, synccore.SourceMissing, ""},
		{faketool.Step{}, synccore.SourceEmpty, ""},
		{faketool.Step{Stdout: "a.txt\nsub/\n"}, synccore.SourceOK, ""},
		{faketool.Step{Exit: 5}, 0, model.KindNetwork},
	} {
		f := newPlanner(t, spec("pull", "mirror"), c.step)
		f.prepare()
		got, err := f.pl.RemoteState(context.Background(), f.env, pair)
		if model.KindOf(err) != c.kind || (c.kind == "" && got != c.want) {
			t.Errorf("exit %d, stdout %q: RemoteState = %v, %v", c.step.Exit, c.step.Stdout, got, err)
		}
		calls := f.tool.Calls()
		if len(calls) != 1 || calls[0].Args[0] != "lsf" || calls[0].Args[1] != "remote:/srv/a" ||
			!calls[0].Has("--config", filepath.Join(f.env.Dir.Path, "rclone.conf")) || !calls[0].Has("--max-depth", "1") ||
			!calls[0].Has("--timeout", "60s") || !calls[0].Has("--contimeout", "15s") {
			t.Errorf("calls = %+v", calls)
		}
	}
}

func TestPlanUsesDryRun(t *testing.T) {
	f := newPlanner(t, spec("push", "mirror"), faketool.Step{Stderr: fixture(t, "dryrun.jsonl")})
	var progress []model.Progress
	f.env.Progress = func(p model.Progress) { progress = append(progress, p) }
	f.prepare()
	plan, err := f.pl.Plan(context.Background(), f.env, pairA)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TransferCount != 1 || plan.DeleteCount != 2 {
		t.Errorf("plan = %+v", plan)
	}
	// A long dry run is not silent: its stats count what it found so far.
	if len(progress) == 0 {
		t.Fatal("no progress from the dry run")
	}
	if last := progress[len(progress)-1]; last.FilesDone != 1 || last.BytesDone != 7 || last.Scanned != 7 {
		t.Errorf("last progress = %+v", last)
	}
	calls := f.tool.Calls()
	if len(calls) != 1 || calls[0].Args[0] != "sync" || !slices.Contains(calls[0].Args, "--dry-run") || slices.Contains(calls[0].Args, "--max-delete") {
		t.Errorf("calls = %+v", calls)
	}
}

func TestTransferResult(t *testing.T) {
	f := newPlanner(t, spec("push", "keep"), faketool.Step{Stderr: fixture(t, "copy.jsonl")})
	var progress []model.Progress
	f.env.Progress = func(p model.Progress) { progress = append(progress, p) }
	f.prepare()
	res, err := f.pl.Transfer(context.Background(), f.env, pairA, -1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Bytes != 12 || res.Files != 2 || res.Status != model.StatusSuccess {
		t.Errorf("result = %+v", res)
	}
	if len(progress) == 0 {
		t.Error("no progress reported")
	}
	if strings.Contains(f.log.String(), "Copied (new)") {
		t.Errorf("per-file info line leaked into log:\n%s", f.log.String())
	}
	if !strings.Contains(f.log.String(), "summary: 2 files transferred, 12 bytes\n") {
		t.Errorf("log lacks summary:\n%s", f.log.String())
	}
	if calls := f.tool.Calls(); len(calls) != 1 || calls[0].Args[0] != "copy" || slices.Contains(calls[0].Args, "--dry-run") {
		t.Errorf("calls = %+v", calls)
	}
}

func TestTransferPassesMaxDelete(t *testing.T) {
	f := newPlanner(t, spec("push", "mirror"))
	f.prepare()
	if _, err := f.pl.Transfer(context.Background(), f.env, pairA, 3); err != nil {
		t.Fatal(err)
	}
	if calls := f.tool.Calls(); len(calls) != 1 || !calls[0].Has("--max-delete", "3") {
		t.Errorf("calls = %+v", calls)
	}
}

func TestTransferFailure(t *testing.T) {
	f := newPlanner(t, spec("push", "keep"), faketool.Step{Exit: 5,
		Stderr: `{"level":"error","msg":"Failed to copy: connection lost","object":"a.txt"}` + "\n"})
	f.prepare()
	_, err := f.pl.Transfer(context.Background(), f.env, pairA, -1)
	if model.KindOf(err) != model.KindNetwork || !strings.Contains(err.Error(), "connection lost") {
		t.Errorf("Transfer = %v", err)
	}
}

// newWebDAVPlanner is a planner on a WebDAV connection whose password
// `rclone obscure` turns into OBSCURED; steps follow that first call.
func newWebDAVPlanner(t *testing.T, s model.SyncSpec, steps ...faketool.Step) *plannerFixture {
	t.Helper()
	s.Protocol = "webdav"
	f := newPlanner(t, s, append([]faketool.Step{{Stdout: "OBSCURED\n"}}, steps...)...)
	f.acc.kind, f.acc.pass = model.ConnWebDAV, "hunter2-pass"
	return f
}

// A WebDAV push creates the remote folder in a call of its own before the
// copy. Within one copy, rclone's directory pass and its transfers would
// all MKCOL a missing remote folder at once, and a server that locks a
// path for each request (golang.org/x/net/webdav, ownCloud) answers the
// losers 423 Locked; the directory pass does not retry that.
func TestWebDAVPushCreatesRemoteFolderFirst(t *testing.T) {
	for _, mode := range []string{"keep", "mirror"} {
		f := newWebDAVPlanner(t, spec("push", mode))
		f.prepare()
		if _, err := f.pl.Transfer(context.Background(), f.env, pairA, -1); err != nil {
			t.Fatal(err)
		}
		calls := f.tool.Calls()
		if len(calls) != 3 || calls[0].Args[0] != "obscure" {
			t.Fatalf("%s: calls = %+v", mode, calls)
		}
		mkdir := calls[1]
		if mkdir.Args[0] != "mkdir" || mkdir.Args[1] != "remote:/srv/a" ||
			!mkdir.Has("--config", filepath.Join(f.env.Dir.Path, "rclone.conf")) ||
			!mkdir.Has("--timeout", "60s") || !mkdir.Has("--contimeout", "15s") || !mkdir.Has("--low-level-retries", "3") {
			t.Errorf("%s: mkdir call = %q", mode, mkdir.Args)
		}
		if sub := map[string]string{"keep": "copy", "mirror": "sync"}[mode]; calls[2].Args[0] != sub {
			t.Errorf("%s: then %q, want %s", mode, calls[2].Args, sub)
		}
		if !strings.Contains(f.log.String(), "$ rclone mkdir remote:/srv/a ") {
			t.Errorf("%s: the mkdir call is not in the log:\n%s", mode, f.log.String())
		}
	}
}

// A pull writes into a local folder, and rclone's SFTP backend makes each
// directory once per process: neither needs the separate call.
func TestOnlyWebDAVPushCreatesRemoteFolder(t *testing.T) {
	for _, c := range []struct {
		protocol, direction string
	}{{"webdav", "pull"}, {"sftp", "push"}, {"sftp", "pull"}} {
		var f *plannerFixture
		if c.protocol == "webdav" {
			f = newWebDAVPlanner(t, spec(c.direction, "keep"))
		} else {
			f = newPlanner(t, spec(c.direction, "keep"))
		}
		f.prepare()
		if _, err := f.pl.Transfer(context.Background(), f.env, pairA, -1); err != nil {
			t.Fatal(err)
		}
		for _, call := range f.tool.Calls() {
			if call.Args[0] == "mkdir" {
				t.Errorf("%s %s: %q", c.protocol, c.direction, call.Args)
			}
		}
	}
}

// A remote folder that cannot be created fails the pair the way the copy
// would have, and nothing is copied.
func TestWebDAVMkdirFailure(t *testing.T) {
	f := newWebDAVPlanner(t, spec("push", "keep"), faketool.Step{Exit: 5,
		Stderr: "2026/11/17 09:12:55 ERROR : Attempt 1/1 failed with 1 errors and: connection refused\n"})
	f.prepare()
	_, err := f.pl.Transfer(context.Background(), f.env, pairA, -1)
	if model.KindOf(err) != model.KindNetwork || !strings.Contains(err.Error(), "rclone mkdir failed (exit 5)") {
		t.Errorf("Transfer = %v", err)
	}
	if calls := f.tool.Calls(); len(calls) != 2 {
		t.Errorf("calls = %+v", calls)
	}
}

// What rclone says when it fails ends up in the run record, which is not
// behind the log's redactor.
func TestFailureMessageRedacted(t *testing.T) {
	f := newWebDAVPlanner(t, spec("push", "keep"), faketool.Step{},
		faketool.Step{Exit: 1, Stderr: `{"level":"error","msg":"auth failed for hunter2-pass"}` + "\n"})
	f.prepare()
	_, err := f.pl.Transfer(context.Background(), f.env, pairA, -1)
	if err == nil || !strings.HasPrefix(err.Error(), "rclone copy failed") || strings.Contains(err.Error(), "hunter2-pass") {
		t.Errorf("Transfer = %v", err)
	}
}

func TestCloseClosesAccess(t *testing.T) {
	f := newPlanner(t, spec("push", "keep"))
	f.prepare()
	if err := f.pl.Close(); err != nil {
		t.Fatal(err)
	}
	if f.acc.releases != 1 {
		t.Errorf("released %d times", f.acc.releases)
	}
	f.pl.Close()
	if f.acc.releases != 1 {
		t.Errorf("released %d times after a second Close", f.acc.releases)
	}
}
