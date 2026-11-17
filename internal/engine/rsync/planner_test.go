package rsync

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"tern/internal/access"
	"tern/internal/engine"
	"tern/internal/engine/faketool"
	"tern/internal/engine/synccore"
	"tern/internal/model"
	"tern/internal/redact"
	"tern/internal/rundir"
)

type fakeAccess struct {
	mu              sync.Mutex
	opens, releases int
}

func (f *fakeAccess) Open(ctx context.Context, connID string, dir *rundir.Dir, red *redact.Redactor) (access.Access, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	return access.Access{Kind: model.ConnSSH, SSHConfig: filepath.Join(dir.Path, "ssh_config")},
		func() { f.mu.Lock(); f.releases++; f.mu.Unlock() }, nil
}

func (f *fakeAccess) Check(ctx context.Context, connID string) error { return nil }

type plannerFixture struct {
	t    *testing.T
	acc  *fakeAccess
	tool *faketool.Fake
	ssh  *faketool.Fake // stops the remote end of a dead attempt
	log  *bytes.Buffer
	env  engine.Env
	pl   synccore.Planner
}

func newPlanner(t *testing.T, s model.SyncSpec, steps ...faketool.Step) *plannerFixture {
	t.Helper()
	dir, err := rundir.New(t.TempDir(), "run1")
	if err != nil {
		t.Fatal(err)
	}
	f := &plannerFixture{t: t, acc: &fakeAccess{}, tool: faketool.New(t, "rsync", steps...), ssh: faketool.New(t, "ssh"), log: &bytes.Buffer{}}
	f.env = engine.Env{Dir: dir, Log: f.log, Redact: redact.New()}
	task := model.Task{ID: "t1", Type: model.TaskSync, Name: "photos", Sync: &s}
	f.pl = NewPlanner(task, model.Connection{ID: "c1", Type: model.ConnSSH, Host: "nas2.example", Port: 22, Username: "backup"},
		Deps{Access: f.acc, Bin: f.tool.Bin, SSHBin: f.ssh.Bin})
	t.Cleanup(func() { f.pl.Close() })
	return f
}

func (f *plannerFixture) prepare() {
	f.t.Helper()
	if err := f.pl.Prepare(context.Background(), f.env); err != nil {
		f.t.Fatal(err)
	}
}

func (f *plannerFixture) sshArg() string {
	return `ssh -F "` + filepath.Join(f.env.Dir.Path, "ssh_config") + `"`
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
	excludes, err := os.ReadFile(filepath.Join(f.env.Dir.Path, "rsync-excludes"))
	if err != nil || string(excludes) != "*.tmp\n/cache/\n" {
		t.Errorf("exclude file = %q, %v", excludes, err)
	}
}

func TestRemoteState(t *testing.T) {
	pair := model.FolderPair{Local: "/targets/a", Remote: "/srv/a"}
	dot := "drwxr-xr-x              0 2026/11/11 08:07:39 .\n"
	for _, c := range []struct {
		step faketool.Step
		want synccore.SourceState
		kind model.ErrKind
	}{
		{faketool.Step{Exit: 23, Stderr: `rsync: [sender] change_dir "/srv/a" failed: No such file or directory (2)` + "\n"}, synccore.SourceMissing, ""},
		{faketool.Step{Stdout: dot}, synccore.SourceEmpty, ""},
		{faketool.Step{Stdout: dot + "-rw-r--r--          1,024 2026/11/11 08:07:39 a file.txt\n"}, synccore.SourceOK, ""},
		{faketool.Step{Exit: 255}, 0, model.KindNetwork},
		{faketool.Step{Exit: 23, Stderr: "rsync: [sender] opendir \"/srv/a\" failed: Permission denied (13)\n"}, 0, model.KindData},
	} {
		f := newPlanner(t, spec("pull", "mirror"), c.step)
		f.prepare()
		got, err := f.pl.RemoteState(context.Background(), f.env, pair)
		if model.KindOf(err) != c.kind || (c.kind == "" && got != c.want) {
			t.Errorf("exit %d, stdout %q: RemoteState = %v, %v", c.step.Exit, c.step.Stdout, got, err)
		}
		calls := f.tool.Calls()
		want := []string{"-s", "--list-only", "--timeout=60", "-e", f.sshArg(), "tern-target:/srv/a/"}
		if len(calls) != 1 || !slices.Equal(calls[0].Args, want) {
			t.Errorf("calls = %+v, want %q", calls, want)
		}
	}
}

func TestPlanDryRun(t *testing.T) {
	f := newPlanner(t, spec("push", "mirror"), faketool.Step{Stdout: fixture(t, "dryrun.txt")})
	f.prepare()
	plan, err := f.pl.Plan(context.Background(), f.env, pairA)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TransferCount != 1 || plan.DeleteCount != 2 || plan.Bytes != 7 {
		t.Errorf("plan = %+v", plan)
	}
	c := f.tool.Calls()[0]
	if !slices.Contains(c.Args, "--dry-run") || !slices.Contains(c.Args, "--delete") {
		t.Errorf("args = %q", c.Args)
	}
	// synccore logs the (capped) plan; the dry run's entry lines stay out.
	if strings.Contains(f.log.String(), "*deleting") {
		t.Errorf("log has the dry run's entries:\n%s", f.log.String())
	}
}

// rsync counts deleted directories against --max-delete; the plan counts
// files. The limit passed on is the files counted plus the directories
// the dry run of the same pair showed.
func TestTransferLimitCountsDirectories(t *testing.T) {
	f := newPlanner(t, spec("push", "mirror"), faketool.Step{Stdout: fixture(t, "dryrun.txt")}, faketool.Step{})
	f.prepare()
	plan, err := f.pl.Plan(context.Background(), f.env, pairA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pl.Transfer(context.Background(), f.env, pairA, plan.DeleteCount); err != nil {
		t.Fatal(err)
	}
	if c := f.tool.Calls()[1]; !slices.Contains(c.Args, "--max-delete=3") || slices.Contains(c.Args, "--dry-run") {
		t.Errorf("args = %q", c.Args)
	}
}

func TestTransferWarningOn24(t *testing.T) {
	f := newPlanner(t, spec("push", "keep"), faketool.Step{Exit: 24, Stderr: "file has vanished: \"/sources/a/tmp.txt\"\n"})
	f.prepare()
	res, err := f.pl.Transfer(context.Background(), f.env, pairA, -1)
	if err != nil || res.Status != model.StatusWarning {
		t.Errorf("Transfer = %+v, %v", res, err)
	}
}

func TestTransferResult(t *testing.T) {
	f := newPlanner(t, spec("push", "keep"), faketool.Step{Stdout: fixture(t, "copy.txt")})
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
	if strings.Contains(f.log.String(), ">f+++++++++ sub/b.txt") {
		t.Errorf("per-file entry leaked into log:\n%s", f.log.String())
	}
	if !strings.Contains(f.log.String(), "Number of regular files transferred: 2") {
		t.Errorf("log lacks stats summary:\n%s", f.log.String())
	}
	if c := f.tool.Calls()[0]; slices.Contains(c.Args, "--dry-run") || slices.Contains(c.Args, "--out-format=%i %n%L") || c.Args[len(c.Args)-1] != "tern-target:/srv/a/" {
		t.Errorf("args = %q", c.Args)
	}
}

func TestTransferFailure(t *testing.T) {
	f := newPlanner(t, spec("push", "keep"), faketool.Step{Exit: 12,
		Stderr: "rsync error: error in rsync protocol data stream (code 12) at io.c(232)\nConnection reset by peer\n"})
	f.prepare()
	_, err := f.pl.Transfer(context.Background(), f.env, pairA, -1)
	if model.KindOf(err) != model.KindNetwork || !strings.HasPrefix(err.Error(), "rsync failed (exit 12): Connection reset by peer") {
		t.Errorf("Transfer = %v", err)
	}
}

func TestCloseClosesAccess(t *testing.T) {
	f := newPlanner(t, spec("push", "keep"))
	f.prepare()
	f.pl.Close()
	f.pl.Close()
	if f.acc.releases != 1 {
		t.Errorf("released %d times", f.acc.releases)
	}
}

// A transfer cut off by a dead link leaves its remote receiver running
// until TCP gives up, holding the half-sent file. The next transfer of the
// pair first stops that receiver (it saves its partial file when it gets
// SIGTERM), so rsync can resume instead of starting the file over.
func TestRetryStopsStaleRemoteReceiver(t *testing.T) {
	f := newPlanner(t, spec("push", "keep"),
		faketool.Step{Exit: 30, Stderr: "tern-pid:4242\n[sender] io timeout after 60 seconds -- exiting\n"},
		faketool.Step{Stderr: "tern-pid:4343\n"},
		faketool.Step{Stderr: "tern-pid:4444\n"})
	f.prepare()
	ctx := context.Background()
	if _, err := f.pl.Transfer(ctx, f.env, pairA, -1); model.KindOf(err) != model.KindNetwork {
		t.Fatalf("first Transfer = %v", err)
	}
	if n := len(f.ssh.Calls()); n != 0 {
		t.Fatalf("ssh called %d times before any retry", n)
	}
	if strings.Contains(f.log.String(), "tern-pid:4242") {
		t.Errorf("the PID line went to the run log:\n%s", f.log.String())
	}

	if _, err := f.pl.Transfer(ctx, f.env, pairA, -1); err != nil {
		t.Fatalf("second Transfer = %v", err)
	}
	calls := f.ssh.Calls()
	if len(calls) != 1 {
		t.Fatalf("ssh called %d times, want once before the retry", len(calls))
	}
	c := calls[0]
	if !c.Has("-F", filepath.Join(f.env.Dir.Path, "ssh_config"), "tern-target", "sh", "-s") || !strings.Contains(c.Stdin, "pid=4242\n") {
		t.Errorf("ssh args = %q, script:\n%s", c.Args, c.Stdin)
	}
	if !strings.Contains(f.log.String(), "4242") {
		t.Errorf("the run log does not say what was stopped:\n%s", f.log.String())
	}

	// The second transfer ended normally: nothing to stop before the third.
	if _, err := f.pl.Transfer(ctx, f.env, pairA, -1); err != nil {
		t.Fatalf("third Transfer = %v", err)
	}
	if n := len(f.ssh.Calls()); n != 1 {
		t.Errorf("ssh called %d times, want still once", n)
	}
}

// The stop script runs in the remote shell: it only signals a process
// that still is an rsync server, its children too, and waits for them.
func TestStopScript(t *testing.T) {
	s := stopScript("4242")
	for _, want := range []string{"pid=4242", "/proc/$pid/cmdline", "*rsync*--server*", "kill -TERM", "kill -0", "kill -KILL"} {
		if !strings.Contains(s, want) {
			t.Errorf("stop script lacks %q:\n%s", want, s)
		}
	}
}

func TestStopScriptRuns(t *testing.T) {
	if _, err := os.Stat("/proc/self/cmdline"); err != nil {
		t.Skip("needs /proc")
	}
	// A process that is not an rsync server is left alone.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	if out, err := exec.Command("sh", "-c", stopScript(strconv.Itoa(cmd.Process.Pid))).CombinedOutput(); err != nil {
		t.Fatalf("stop script: %v\n%s", err, out)
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("the script stopped a process that is not rsync: %v", err)
	}

	// One that is, with a child, is stopped, child and all.
	fake := filepath.Join(t.TempDir(), "rsync")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 30 &\nwait\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := exec.Command(fake, "--server", "-s", ".")
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Wait() }()
	time.Sleep(200 * time.Millisecond) // let it start its child
	if out, err := exec.Command("sh", "-c", stopScript(strconv.Itoa(server.Process.Pid))).CombinedOutput(); err != nil {
		t.Fatalf("stop script: %v\n%s", err, out)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		server.Process.Kill()
		t.Fatal("the rsync server is still running after the stop script")
	}
}
