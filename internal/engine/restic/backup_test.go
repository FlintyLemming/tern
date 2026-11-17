package restic

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"tern/internal/access"
	"tern/internal/engine"
	"tern/internal/engine/filter"
	"tern/internal/engine/restic/restictest"
	"tern/internal/model"
	"tern/internal/redact"
	"tern/internal/rundir"
	"tern/internal/store"
	"tern/internal/vault"
)

const repoPW = `repo-pw'"$s3cret`

// fakeAccess stands in for access.Opener: no agent, no network.
type fakeAccess struct {
	mu                    sync.Mutex
	checkErr, openErr     error
	checks, opens, closes int
}

func (f *fakeAccess) Open(ctx context.Context, connID string, dir *rundir.Dir, red *redact.Redactor) (access.Access, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openErr != nil {
		return access.Access{}, nil, f.openErr
	}
	f.opens++
	return access.Access{Kind: model.ConnSSH, SSHConfig: filepath.Join(dir.Path, "ssh_config")}, func() {
		f.mu.Lock()
		f.closes++
		f.mu.Unlock()
	}, nil
}

func (f *fakeAccess) Check(ctx context.Context, connID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return f.checkErr
}

type fx struct {
	t    *testing.T
	st   *store.Store
	v    *vault.Vault
	acc  *fakeAccess
	fake *restictest.Fake
	deps Deps
	task model.Task // change it before build
	conn model.Connection
	env  engine.Env
	log  *bytes.Buffer
	out  io.WriteCloser

	mu       sync.Mutex
	progress []model.Progress
}

var (
	stepOK     = fakeStep{}
	backupDone = fakeStep{Stdout: `{"message_type":"status","seconds_elapsed":2,"total_files":10,"files_done":5,"total_bytes":1000,"bytes_done":500}` + "\n" +
		`{"message_type":"summary","files_new":3,"files_changed":1,"files_unmodified":6,"data_added":4096,"total_files_processed":10,"total_bytes_processed":1000,"snapshot_id":"abcd1234"}` + "\n"}
)

// newFx stores a backup task with one non-empty source directory and a
// sealed repository password, and scripts the fake restic with steps.
func newFx(t *testing.T, steps ...fakeStep) *fx {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	v := vault.New(st, t.TempDir())
	if _, err := v.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := v.Init(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC)
	key := model.SSHKey{ID: model.NewID(), Version: 1, Name: "k", PublicKey: "ssh-ed25519 AAAA", Fingerprint: "SHA256:x", CreatedAt: now, UpdatedAt: now}
	if err := st.CreateKey(ctx, key, "v1.sealed", ""); err != nil {
		t.Fatal(err)
	}
	conn := model.Connection{ID: model.NewID(), Version: 1, Type: model.ConnSSH, Name: "nas2", Host: "nas2.example", Port: 22,
		Username: "backup", SSHKeyID: key.ID, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateConnection(ctx, conn, ""); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(t.TempDir(), "photos")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	task := model.Task{
		ID: model.NewID(), Version: 1, Type: model.TaskBackup, Name: "photos", Enabled: true, CreatedAt: now, UpdatedAt: now,
		Backup: &model.BackupSpec{ConnectionID: conn.ID, RepoPath: "/srv/repo", Host: "nas-1", Sources: []string{src}},
	}
	task.ApplyDefaults()
	sealed, err := v.Seal("tasks/"+task.ID+"/repo_password", []byte(repoPW))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(ctx, task, sealed); err != nil {
		t.Fatal(err)
	}

	f := &fx{t: t, st: st, v: v, acc: &fakeAccess{}, fake: restictest.New(t, steps...), task: task, conn: conn, log: &bytes.Buffer{}}
	f.deps = Deps{Access: f.acc, Store: st, Vault: v, DataDir: t.TempDir(), Bin: f.fake.Bin}
	red := redact.New()
	f.out = red.Writer(f.log)
	f.env = engine.Env{
		Run: model.Run{ID: "run1", TaskID: task.ID, Kind: model.RunBackup},
		Dir: newRunDir(t), Log: f.out, Redact: red,
		Progress: func(p model.Progress) {
			f.mu.Lock()
			f.progress = append(f.progress, p)
			f.mu.Unlock()
		},
	}
	return f
}

func (f *fx) src() string { return f.task.Backup.Sources[0] }

// build makes the engine for one run of f.task as it is now.
func (f *fx) build(kind model.RunKind, params map[string]string) engine.Engine {
	f.t.Helper()
	eng, err := NewBuilder(f.deps)(f.task, engine.Request{Kind: kind, Params: params})
	if err != nil {
		f.t.Fatalf("build %s engine: %v", kind, err)
	}
	f.t.Cleanup(func() { eng.(io.Closer).Close() })
	return eng
}

// ready returns an engine whose Preflight has passed.
func (f *fx) ready(kind model.RunKind, params map[string]string) engine.Engine {
	f.t.Helper()
	eng := f.build(kind, params)
	if err := eng.Preflight(context.Background(), f.env); err != nil {
		f.t.Fatalf("Preflight: %v", err)
	}
	return eng
}

func (f *fx) logged() string {
	f.out.Close() // flush a last line without newline
	return f.log.String()
}

// call returns the first recorded call of subcommand sub.
func (f *fx) call(sub string) fakeCall {
	f.t.Helper()
	for _, c := range f.fake.Calls() {
		if c.Sub() == sub {
			return c
		}
	}
	f.t.Fatalf("no %q call; calls were %q", sub, f.fake.Subs())
	return fakeCall{}
}

func wantKind(t *testing.T, err error, kind model.ErrKind, msgPart string) {
	t.Helper()
	var ke *model.KindError
	if !errors.As(err, &ke) || ke.Kind != kind {
		t.Fatalf("err = %v, want a %s error", err, kind)
	}
	if !strings.Contains(err.Error(), msgPart) {
		t.Errorf("err = %q, want it to contain %q", err, msgPart)
	}
}

func TestBuilderRejects(t *testing.T) {
	f := newFx(t)
	b := NewBuilder(f.deps)
	if _, err := b(f.task, engine.Request{Kind: model.RunSync}); model.KindOf(err) != model.KindConfig {
		t.Errorf("sync run of a backup task: %v", err)
	}
	noSpec := f.task
	noSpec.Backup = nil
	if _, err := b(noSpec, engine.Request{Kind: model.RunBackup}); model.KindOf(err) != model.KindConfig {
		t.Errorf("task without backup spec: %v", err)
	}
	gone := f.task
	spec := *f.task.Backup
	spec.ConnectionID = "nope"
	gone.Backup = &spec
	if _, err := b(gone, engine.Request{Kind: model.RunBackup}); model.KindOf(err) != model.KindConfig {
		t.Errorf("task with unknown connection: %v", err)
	}
}

func TestBackupUnits(t *testing.T) {
	f := newFx(t)
	if got := f.build(model.RunBackup, nil).Units(); !reflect.DeepEqual(got, []string{"backup"}) {
		t.Errorf("Units = %q", got)
	}
}

func TestPreflightMissingSource(t *testing.T) {
	f := newFx(t)
	f.task.Backup.Sources = []string{f.src(), "/sources/photos"}
	err := f.build(model.RunBackup, nil).Preflight(context.Background(), f.env)
	wantKind(t, err, model.KindConfig, "source missing: /sources/photos")
	if n := len(f.fake.Calls()); n != 0 || f.acc.checks != 0 {
		t.Errorf("%d restic calls, %d access checks; want none", n, f.acc.checks)
	}
}

func TestPreflightSourceIsFile(t *testing.T) {
	f := newFx(t)
	file := filepath.Join(f.src(), "a.jpg")
	f.task.Backup.Sources = []string{file}
	wantKind(t, f.build(model.RunBackup, nil).Preflight(context.Background(), f.env), model.KindConfig, "source missing: "+file)
}

func TestPreflightEmptySource(t *testing.T) {
	f := newFx(t)
	empty := t.TempDir()
	f.task.Backup.Sources = []string{f.src(), empty}
	err := f.build(model.RunBackup, nil).Preflight(context.Background(), f.env)
	wantKind(t, err, model.KindConfig, "source is empty: "+empty+" (is the share mounted?)")
	if n := len(f.fake.Calls()); n != 0 {
		t.Errorf("%d restic calls, want none", n)
	}
}

func TestPreflightRepoMissing(t *testing.T) {
	f := newFx(t, fakeStep{Exit: 10, Stderr: "Fatal: repository does not exist\n"})
	wantKind(t, f.build(model.RunBackup, nil).Preflight(context.Background(), f.env), model.KindConfig, "repository")
	if c := f.call("cat"); !c.Has("cat", "config") {
		t.Errorf("preflight call = %q", c.Args)
	}
}

func TestPreflightWrongPassword(t *testing.T) {
	f := newFx(t, fakeStep{Exit: 12})
	wantKind(t, f.build(model.RunBackup, nil).Preflight(context.Background(), f.env), model.KindAuth, "password")
}

func TestPreflightNetwork(t *testing.T) {
	f := newFx(t)
	f.acc.checkErr = &model.KindError{Kind: model.KindNetwork, Msg: "nas2.example:22 unreachable"}
	wantKind(t, f.build(model.RunBackup, nil).Preflight(context.Background(), f.env), model.KindNetwork, "unreachable")
	if n := len(f.fake.Calls()); n != 0 || f.acc.opens != 0 {
		t.Errorf("%d restic calls, %d opens; want none", n, f.acc.opens)
	}
}

func TestPreflightNoPassword(t *testing.T) {
	f := newFx(t)
	if _, err := f.st.DB().Exec("UPDATE tasks SET repo_password = ''"); err != nil {
		t.Fatal(err)
	}
	wantKind(t, f.build(model.RunBackup, nil).Preflight(context.Background(), f.env), model.KindConfig, "password")
	if n := len(f.fake.Calls()); n != 0 {
		t.Errorf("%d restic calls, want none", n)
	}
}

func runBackup(t *testing.T, f *fx) (model.UnitResult, error) {
	t.Helper()
	eng := f.ready(model.RunBackup, nil)
	return eng.RunUnit(context.Background(), f.env, 0)
}

func TestBackupArgs(t *testing.T) {
	f := newFx(t, stepOK, stepOK, backupDone)
	second := t.TempDir()
	os.WriteFile(filepath.Join(second, "f"), nil, 0o644)
	f.task.Backup.Sources = append(f.task.Backup.Sources, second)
	f.task.Backup.LimitUploadKiB = 1024
	if _, err := runBackup(t, f); err != nil {
		t.Fatal(err)
	}
	c := f.call("backup")
	excl := filepath.Join(f.env.Dir.Path, "excludes")
	for _, want := range [][]string{{"--json"}, {"--host", "nas-1"}, {"--tag", "tern-" + f.task.ID}, {"--exclude-file", excl}, {"--limit-upload", "1024"}} {
		if !c.Has(want...) {
			t.Errorf("backup args %q lack %q", c.Args, want)
		}
	}
	if tail := c.Args[len(c.Args)-2:]; !reflect.DeepEqual(tail, f.task.Backup.Sources) {
		t.Errorf("args end with %q, want the sources %q", tail, f.task.Backup.Sources)
	}
	if !c.Has("-r", "sftp:tern-target:/srv/repo") || !c.Has("-o", "sftp.connections=5") {
		t.Errorf("global options missing in %q", c.Args)
	}
	if c.Env["RESTIC_CACHE_DIR"] != filepath.Join(f.deps.DataDir, "restic-cache") {
		t.Errorf("env = %v", c.Env)
	}
}

func TestNoLimitFlagWhenZero(t *testing.T) {
	f := newFx(t, stepOK, stepOK, backupDone)
	if _, err := runBackup(t, f); err != nil {
		t.Fatal(err)
	}
	for _, a := range f.call("backup").Args {
		if a == "--limit-upload" {
			t.Error("--limit-upload passed without a limit")
		}
	}
}

func TestHostFromSpec(t *testing.T) {
	f := newFx(t, stepOK, stepOK, backupDone, stepOK)
	f.task.Backup.Host = "fixed-at-creation"
	f.task.Backup.Keep.Last = 1
	if hn, _ := os.Hostname(); hn == f.task.Backup.Host {
		t.Skip("this machine really is called that")
	}
	if _, err := runBackup(t, f); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"backup", "forget"} {
		if c := f.call(sub); !c.Has("--host", "fixed-at-creation") {
			t.Errorf("%s args = %q", sub, c.Args)
		}
	}
}

func TestExcludeFileContent(t *testing.T) {
	f := newFx(t, stepOK, stepOK, backupDone)
	f.task.Backup.Excludes = []string{"*.tmp", "/cache/", "pri$ce"}
	if _, err := runBackup(t, f); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(f.env.Dir.Path, "excludes"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(filter.ToRestic(f.task.Backup.Excludes, f.task.Backup.Sources), "\n") + "\n"
	if string(got) != want {
		t.Errorf("exclude file = %q, want %q", got, want)
	}
}

func TestCallOrder(t *testing.T) {
	f := newFx(t, stepOK, stepOK, backupDone, stepOK)
	f.task.Backup.Keep.Last = 3
	if _, err := runBackup(t, f); err != nil {
		t.Fatal(err)
	}
	if got := f.fake.Subs(); !reflect.DeepEqual(got, []string{"cat", "unlock", "backup", "forget"}) {
		t.Errorf("calls = %q", got)
	}
}

func TestUnlockFailureDoesNotStopBackup(t *testing.T) {
	f := newFx(t, stepOK, fakeStep{Exit: 1, Stderr: "unlock went wrong\n"}, backupDone)
	res, err := runBackup(t, f)
	if err != nil || res.Status != model.StatusSuccess {
		t.Fatalf("RunUnit = %+v, %v", res, err)
	}
	if !strings.Contains(f.logged(), "unlock went wrong") {
		t.Errorf("log = %q", f.log)
	}
}

func TestForgetArgs(t *testing.T) {
	f := newFx(t, stepOK, stepOK, backupDone, stepOK)
	f.task.Backup.Keep = model.KeepPolicy{Last: 7, Weekly: 4}
	if _, err := runBackup(t, f); err != nil {
		t.Fatal(err)
	}
	c := f.call("forget")
	for _, want := range [][]string{{"--keep-last", "7"}, {"--keep-weekly", "4"}, {"--group-by", "host,tags"}, {"--host", "nas-1"}, {"--tag", "tern-" + f.task.ID}} {
		if !c.Has(want...) {
			t.Errorf("forget args %q lack %q", c.Args, want)
		}
	}
	for _, a := range c.Args {
		if a == "--keep-daily" || a == "--keep-monthly" || a == "--prune" {
			t.Errorf("forget args %q contain %s", c.Args, a)
		}
	}
}

func TestNoForgetWhenKeepUnset(t *testing.T) {
	f := newFx(t, stepOK, stepOK, backupDone)
	if _, err := runBackup(t, f); err != nil {
		t.Fatal(err)
	}
	if got := f.fake.Subs(); !reflect.DeepEqual(got, []string{"cat", "unlock", "backup"}) {
		t.Errorf("calls = %q", got)
	}
}

func TestForgetFailureIsWarning(t *testing.T) {
	f := newFx(t, stepOK, stepOK, backupDone, fakeStep{Exit: 1, Stderr: "forget broke\n"})
	f.task.Backup.Keep.Daily = 7
	res, err := runBackup(t, f)
	if err != nil || res.Status != model.StatusWarning || res.Bytes != 4096 {
		t.Fatalf("RunUnit = %+v, %v; want a warning and no error", res, err)
	}
}

func TestExit3IsWarning(t *testing.T) {
	f := newFx(t, stepOK, stepOK, fakeStep{Exit: 3, Stdout: `{"message_type":"error","error":{"message":"open /x: permission denied"},"during":"archival","item":"/x"}` + "\n" + backupDone.Stdout}, stepOK)
	f.task.Backup.Keep.Last = 1
	res, err := runBackup(t, f)
	if err != nil || res.Status != model.StatusWarning {
		t.Fatalf("RunUnit = %+v, %v; want a warning and no error", res, err)
	}
	if got := f.fake.Subs(); got[len(got)-1] != "forget" {
		t.Errorf("calls = %q, want forget after an incomplete backup", got)
	}
	if !strings.Contains(f.logged(), "open /x: permission denied") {
		t.Errorf("the unreadable file is not in the log: %q", f.log)
	}
}

func TestExit11Retryable(t *testing.T) {
	f := newFx(t, stepOK, stepOK, fakeStep{Exit: 11, Stderr: "unable to create lock in backend: repository is already locked\n"}, stepOK)
	f.task.Backup.Keep.Last = 1
	_, err := runBackup(t, f)
	if err == nil || !model.KindOf(err).Retryable() {
		t.Fatalf("err = %v (kind %q), want a retryable error", err, model.KindOf(err))
	}
	if got := f.fake.Subs(); !reflect.DeepEqual(got, []string{"cat", "unlock", "backup"}) {
		t.Errorf("calls = %q; a failed backup must not be followed by forget", got)
	}
}

func TestBackupFailureKinds(t *testing.T) {
	f := newFx(t, stepOK, stepOK, fakeStep{Exit: 1, Stderr: "subprocess ssh: Connection reset by peer\n"})
	_, err := runBackup(t, f)
	wantKind(t, err, model.KindNetwork, "Connection reset by peer")
}

func TestNeverRemoveAll(t *testing.T) {
	f := newFx(t, stepOK, stepOK, fakeStep{Exit: 11}, stepOK, stepOK, stepOK, stepOK, backupDone, stepOK)
	f.task.Backup.Keep.Last = 1
	eng := f.ready(model.RunBackup, nil)
	ctx := context.Background()
	eng.RunUnit(ctx, f.env, 0)
	eng.Cleanup(ctx, f.env)
	eng.Preflight(ctx, f.env)
	if _, err := eng.RunUnit(ctx, f.env, 0); err != nil {
		t.Fatal(err)
	}
	calls := f.fake.Calls()
	if got := subs(calls); !reflect.DeepEqual(got, []string{"cat", "unlock", "backup", "unlock", "repair", "cat", "unlock", "backup", "forget"}) {
		t.Fatalf("calls = %q", got)
	}
	for _, c := range calls {
		for _, a := range c.Args {
			if strings.Contains(a, "remove-all") {
				t.Errorf("%q", c.Args)
			}
		}
	}
}

func TestUnitResultNumbers(t *testing.T) {
	f := newFx(t, stepOK, stepOK, backupDone)
	res, err := runBackup(t, f)
	want := model.UnitResult{Name: "backup", Status: model.StatusSuccess, Bytes: 4096, Files: 4}
	if err != nil || res != want {
		t.Errorf("RunUnit = %+v, %v; want %+v", res, err, want)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.progress) != 1 || f.progress[0].Unit != "backup" || f.progress[0].BytesDone != 500 || f.progress[0].SpeedBps != 250 {
		t.Errorf("progress = %+v", f.progress)
	}
}

func TestPasswordHandling(t *testing.T) {
	f := newFx(t, stepOK, stepOK, fakeStep{Stdout: backupDone.Stdout, Stderr: "debug: password is " + repoPW + "\n"}, stepOK)
	f.task.Backup.Keep.Last = 1
	if _, err := runBackup(t, f); err != nil {
		t.Fatal(err)
	}
	calls := f.fake.Calls()
	if len(calls) != 4 {
		t.Fatalf("calls = %q", subs(calls))
	}
	pwFile := filepath.Join(f.env.Dir.Path, "repo-password")
	for _, c := range calls {
		for _, a := range c.Args {
			if strings.Contains(a, repoPW) {
				t.Errorf("password in argv of %s", c.Sub())
			}
		}
		for k, v := range c.Env {
			if strings.Contains(v, repoPW) {
				t.Errorf("password in %s of %s", k, c.Sub())
			}
		}
		if c.Env["RESTIC_PASSWORD_FILE"] != pwFile || c.Password != repoPW || c.PasswordMode != 0o600 {
			t.Errorf("%s: password file %q holds %q with mode %o", c.Sub(), c.Env["RESTIC_PASSWORD_FILE"], c.Password, c.PasswordMode)
		}
	}
	// The engine registered the password, so it cannot reach the log,
	// whether a tool prints it or tern itself does.
	io.WriteString(f.env.Log, "tern says "+repoPW+"\n")
	if log := f.logged(); strings.Contains(log, repoPW) || !strings.Contains(log, "password is ***") || !strings.Contains(log, "tern says ***") {
		t.Errorf("log = %q", log)
	}
}

func TestCleanupUnlocks(t *testing.T) {
	f := newFx(t)
	eng := f.ready(model.RunBackup, nil)
	before := len(f.fake.Calls())
	if err := eng.Cleanup(context.Background(), f.env); err != nil {
		t.Fatal(err)
	}
	if got := f.fake.Subs()[before:]; !reflect.DeepEqual(got, []string{"unlock"}) {
		t.Errorf("Cleanup called %q", got)
	}
}

func TestCleanupBeforeAnythingOpened(t *testing.T) {
	f := newFx(t)
	f.acc.checkErr = &model.KindError{Kind: model.KindNetwork, Msg: "down"}
	eng := f.build(model.RunBackup, nil)
	eng.Preflight(context.Background(), f.env)
	if err := eng.Cleanup(context.Background(), f.env); err != nil {
		t.Errorf("Cleanup = %v", err)
	}
	if n := len(f.fake.Calls()); n != 0 {
		t.Errorf("%d restic calls without access to the repository", n)
	}
}

func TestAccessOpenedOnceClosedOnce(t *testing.T) {
	f := newFx(t)
	eng, err := NewBuilder(f.deps)(f.task, engine.Request{Kind: model.RunBackup})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ { // the second one is a retry
		if err := eng.Preflight(ctx, f.env); err != nil {
			t.Fatal(err)
		}
	}
	if f.acc.opens != 1 || f.acc.checks != 2 {
		t.Errorf("opens = %d, checks = %d; want 1 and 2", f.acc.opens, f.acc.checks)
	}
	closer := eng.(io.Closer)
	closer.Close()
	closer.Close()
	if f.acc.closes != 1 {
		t.Errorf("closes = %d, want 1", f.acc.closes)
	}
}

func TestDescribe(t *testing.T) {
	f := newFx(t)
	f.task.Backup.Sources = []string{"/sources/photos"}
	lines := f.build(model.RunBackup, nil).Describe()
	want := []string{
		"export RESTIC_REPOSITORY='sftp:backup@nas2.example:/srv/repo'",
		"export RESTIC_PASSWORD='<repository password>'",
		"restic backup --host nas-1 --tag tern-" + f.task.ID + " /sources/photos",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("Describe =\n%q\nwant\n%q", lines, want)
	}
	if strings.Contains(strings.Join(lines, "\n"), repoPW) {
		t.Error("Describe shows the real password")
	}
	if n := len(f.fake.Calls()); n != 0 || f.acc.opens != 0 {
		t.Error("Describe touched the repository")
	}
}

func TestDescribeQuotes(t *testing.T) {
	f := newFx(t)
	f.task.Backup.Sources = []string{"/sources/my photos", "/sources/it's"}
	lines := f.build(model.RunBackup, nil).Describe()
	if want := "restic backup --host nas-1 --tag tern-" + f.task.ID + ` '/sources/my photos' '/sources/it'\''s'`; lines[2] != want {
		t.Errorf("line 3 = %q, want %q", lines[2], want)
	}
}

// What an interrupted backup uploaded is in the repository but in no
// index, and restic would upload it again. Before the retry the index is
// rebuilt so the next attempt finds it.
func TestRepairIndexBeforeRetry(t *testing.T) {
	f := newFx(t, stepOK, stepOK, fakeStep{Exit: 1, Stderr: "subprocess ssh: Connection reset by peer\n"})
	eng := f.ready(model.RunBackup, nil)
	ctx := context.Background()
	if _, err := eng.RunUnit(ctx, f.env, 0); model.KindOf(err) != model.KindNetwork {
		t.Fatalf("first attempt: %v", err)
	}
	before := len(f.fake.Calls())
	if err := eng.Cleanup(ctx, f.env); err != nil {
		t.Fatal(err)
	}
	calls := f.fake.Calls()[before:]
	if !reflect.DeepEqual(subs(calls), []string{"unlock", "repair"}) || !calls[1].Has("repair", "index") {
		t.Fatalf("Cleanup called %q", subs(calls))
	}
	// Once is enough: a second Cleanup without another failed backup
	// only unlocks.
	before = len(f.fake.Calls())
	eng.Cleanup(ctx, f.env)
	if got := f.fake.Subs()[before:]; !reflect.DeepEqual(got, []string{"unlock"}) {
		t.Errorf("second Cleanup called %q", got)
	}
}

func (f *fx) priorBackup(st model.Status) {
	f.t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	run := model.Run{ID: model.NewID(), TaskID: f.task.ID, TaskName: f.task.Name, Kind: model.RunBackup, Trigger: model.TriggerManual,
		Status: model.StatusQueued, StartedAt: at}
	if err := f.st.CreateRun(ctx, run); err != nil {
		f.t.Fatal(err)
	}
	run.Status, run.FinishedAt = st, at
	if _, err := f.st.FinalizeRun(ctx, run); err != nil {
		f.t.Fatal(err)
	}
}

// The same after a run that was canceled, interrupted or failed: the next
// run rebuilds the index before it backs up.
func TestRepairIndexAfterUnfinishedRun(t *testing.T) {
	for _, st := range []model.Status{model.StatusCanceled, model.StatusInterrupted, model.StatusFailed} {
		f := newFx(t, stepOK, stepOK, stepOK, backupDone)
		f.priorBackup(st)
		if _, err := runBackup(t, f); err != nil {
			t.Fatal(err)
		}
		if got := f.fake.Subs(); !reflect.DeepEqual(got, []string{"cat", "unlock", "repair", "backup"}) {
			t.Errorf("after a %s run, calls = %q", st, got)
		}
	}
	for _, st := range []model.Status{model.StatusSuccess, model.StatusWarning, model.StatusSkipped} {
		f := newFx(t, stepOK, stepOK, backupDone)
		f.priorBackup(model.StatusFailed) // older, and followed by a good one
		f.priorBackup(st)
		if _, err := runBackup(t, f); err != nil {
			t.Fatal(err)
		}
		if got := f.fake.Subs(); !reflect.DeepEqual(got, []string{"cat", "unlock", "backup"}) {
			t.Errorf("after a %s run, calls = %q", st, got)
		}
	}
}

func TestRepairIndexFailureDoesNotStopBackup(t *testing.T) {
	f := newFx(t, stepOK, stepOK, fakeStep{Exit: 11}, backupDone)
	f.priorBackup(model.StatusCanceled)
	res, err := runBackup(t, f)
	if err != nil || res.Status != model.StatusSuccess {
		t.Fatalf("RunUnit = %+v, %v", res, err)
	}
	if got := f.fake.Subs(); !reflect.DeepEqual(got, []string{"cat", "unlock", "repair", "backup"}) {
		t.Errorf("calls = %q", got)
	}
}
