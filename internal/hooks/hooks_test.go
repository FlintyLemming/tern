package hooks

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"tern/internal/access"
	"tern/internal/model"
	"tern/internal/proc"
	"tern/internal/sshx"
	"tern/internal/store"
	"tern/internal/vault"
)

func init() { proc.KillGrace = 200 * time.Millisecond }

var vars = Vars{TaskID: "t1", TaskName: "照片", TaskType: "sync", Trigger: "manual"}

func local(script string) model.Hook {
	return model.Hook{Script: script, Where: model.HookLocal, TimeoutSec: 600}
}

func runLocal(t *testing.T, h model.Hook, v Vars) (string, error) {
	t.Helper()
	var log bytes.Buffer
	err := Runner{}.Run(context.Background(), h, v, &log)
	return log.String(), err
}

func TestEnviron(t *testing.T) {
	want := []string{"TERN_TASK_ID=t1", "TERN_TASK_NAME=照片", "TERN_TASK_TYPE=sync", "TERN_TRIGGER=manual"}
	if got := Environ(vars); !reflect.DeepEqual(got, want) {
		t.Errorf("Environ = %q", got)
	}
}

func TestEnvironWithResult(t *testing.T) {
	v := vars
	v.Result = &ResultVars{Status: "failed", DurationSec: 12, Bytes: 34, Files: 5, Error: "boom"}
	want := []string{"TERN_TASK_ID=t1", "TERN_TASK_NAME=照片", "TERN_TASK_TYPE=sync", "TERN_TRIGGER=manual",
		"TERN_RESULT=failed", "TERN_DURATION_SECONDS=12", "TERN_BYTES=34", "TERN_FILES=5", "TERN_ERROR=boom"}
	if got := Environ(v); !reflect.DeepEqual(got, want) {
		t.Errorf("Environ = %q", got)
	}
}

func TestLocalHookSeesVars(t *testing.T) {
	log, err := runLocal(t, local(`echo "id=$TERN_TASK_ID name=$TERN_TASK_NAME"`), vars)
	if err != nil || !strings.Contains(log, "id=t1 name=照片") {
		t.Errorf("log %q, err %v", log, err)
	}
}

func TestLocalHookNoInheritedEnv(t *testing.T) {
	t.Setenv("TERN_TEST_CANARY", "leak")
	log, err := runLocal(t, local("env"), vars)
	if err != nil || strings.Contains(log, "leak") || !strings.Contains(log, "TERN_TASK_ID=t1") {
		t.Errorf("log %q, err %v", log, err)
	}
}

func TestHookNonZero(t *testing.T) {
	_, err := runLocal(t, local("exit 3"), vars)
	if err == nil || !strings.Contains(err.Error(), "exit 3") {
		t.Errorf("err = %v, want it to mention exit 3", err)
	}
}

func TestHookTimeout(t *testing.T) {
	h := local("sleep 30")
	h.TimeoutSec = 1
	start := time.Now()
	_, err := runLocal(t, h, vars)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("err = %v, want a timeout", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("took %v", took)
	}
}

func TestHookCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Runner{}.Run(ctx, local("sleep 30"), vars, &bytes.Buffer{})
	if err == nil || strings.Contains(err.Error(), "timeout") {
		t.Errorf("err = %v, want a cancellation that is not called a timeout", err)
	}
}

func TestEmptyScriptIsNoop(t *testing.T) {
	// A Runner this broken would fail if it tried to start anything.
	r := Runner{SSHBin: "/nonexistent"}
	for _, h := range []model.Hook{{}, {Where: model.HookSSH, ConnectionID: "c"}, {Script: " \n\t"}} {
		if err := r.Run(context.Background(), h, vars, nil); err != nil {
			t.Errorf("%+v: %v", h, err)
		}
	}
}

func TestOutputGoesToLog(t *testing.T) {
	log, err := runLocal(t, local("echo out; echo err >&2"), vars)
	if err != nil || !strings.Contains(log, "out\n") || !strings.Contains(log, "err\n") {
		t.Errorf("log %q, err %v", log, err)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Errorf("shellQuote = %s", got)
	}
	for _, v := range []string{"", "plain", "it's", "a\nb", "$HOME `id` \"x\" \\", "''", "-n", "照片 %s"} {
		out, err := exec.Command("/bin/sh", "-c", "printf %s "+shellQuote(v)).Output()
		if err != nil || string(out) != v {
			t.Errorf("%q came back as %q (%v)", v, out, err)
		}
	}
}

// fakeSSH writes a script standing in for ssh that records its arguments
// and stdin, and returns its path and the two record files.
func fakeSSH(t *testing.T) (bin, argvFile, stdinFile string) {
	t.Helper()
	dir := t.TempDir()
	bin, argvFile, stdinFile = filepath.Join(dir, "ssh"), filepath.Join(dir, "argv"), filepath.Join(dir, "stdin")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + shellQuote(argvFile) +
		"\ncat > " + shellQuote(stdinFile) + "\necho remote-out\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, argvFile, stdinFile
}

// sshOpener is an Opener over a store holding one SSH connection.
func sshOpener(t *testing.T) (access.Opener, string) {
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
	privPEM, p, err := sshx.Generate()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	keyID, connID := model.NewID(), model.NewID()
	sealed, err := v.Seal("ssh_keys/"+keyID+"/private", privPEM)
	if err != nil {
		t.Fatal(err)
	}
	k := model.SSHKey{ID: keyID, Version: 1, Name: "k", PublicKey: p.Authorized, Fingerprint: p.Fingerprint, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateKey(ctx, k, sealed, ""); err != nil {
		t.Fatal(err)
	}
	c := model.Connection{ID: connID, Version: 1, Type: model.ConnSSH, Name: "c", Host: "nas.example", Port: 22,
		Username: "backup", SSHKeyID: keyID, HostKey: p.Authorized, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateConnection(ctx, c, ""); err != nil {
		t.Fatal(err)
	}
	return access.Opener{Store: st, Vault: v}, connID
}

// shortTempDir avoids the unix socket path limit that t.TempDir can exceed.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "th")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestRemoteCommandShape(t *testing.T) {
	opener, connID := sshOpener(t)
	bin, argvFile, stdinFile := fakeSSH(t)
	runtime := shortTempDir(t)
	r := Runner{Access: opener, RuntimeDir: runtime, SSHBin: bin}
	script := "echo \"hello $TERN_TASK_NAME\"\nexit 0\n"
	h := model.Hook{Script: script, Where: model.HookSSH, ConnectionID: connID, TimeoutSec: 60}
	v := Vars{TaskID: "t1", TaskName: "it's 照片", TaskType: "sync", Trigger: "schedule"}
	var log bytes.Buffer
	if err := r.Run(context.Background(), h, v, &log); err != nil {
		t.Fatal(err)
	}

	argv, _ := os.ReadFile(argvFile)
	args := strings.Split(strings.TrimSuffix(string(argv), "\n"), "\n")
	if len(args) != 5 || args[0] != "-F" || !strings.HasPrefix(args[1], runtime+"/") || filepath.Base(args[1]) != "ssh_config" ||
		!reflect.DeepEqual(args[2:], []string{"tern-target", "sh", "-s"}) {
		t.Errorf("argv = %q", args)
	}
	stdin, _ := os.ReadFile(stdinFile)
	wantHead := "export TERN_TASK_ID='t1'\nexport TERN_TASK_NAME='it'\\''s 照片'\nexport TERN_TASK_TYPE='sync'\nexport TERN_TRIGGER='schedule'\n"
	if string(stdin) != wantHead+script {
		t.Errorf("stdin = %q\nwant    %q", stdin, wantHead+script)
	}
	if !strings.Contains(log.String(), "remote-out") {
		t.Errorf("log = %q", log.String())
	}
	if left, _ := os.ReadDir(runtime); len(left) != 0 {
		t.Errorf("run directory not removed: %v", left)
	}
}

func TestRemoteHookUnknownConnection(t *testing.T) {
	opener, _ := sshOpener(t)
	bin, argvFile, _ := fakeSSH(t)
	runtime := shortTempDir(t)
	r := Runner{Access: opener, RuntimeDir: runtime, SSHBin: bin}
	h := model.Hook{Script: "true", Where: model.HookSSH, ConnectionID: "missing", TimeoutSec: 60}
	if err := r.Run(context.Background(), h, vars, &bytes.Buffer{}); err == nil {
		t.Error("want an error for a hook on a missing connection")
	}
	if _, err := os.Stat(argvFile); err == nil {
		t.Error("ssh was started anyway")
	}
	if left, _ := os.ReadDir(runtime); len(left) != 0 {
		t.Errorf("run directory not removed: %v", left)
	}
}
