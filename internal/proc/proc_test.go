package proc

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func init() { KillGrace = 200 * time.Millisecond }

// lines collects callback output; callbacks may come from two goroutines.
type lines struct {
	mu  sync.Mutex
	got []string
}

func (l *lines) add(line []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, string(line))
}

func (l *lines) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.got...)
}

func sh(script string) Cmd { return Cmd{Path: "/bin/sh", Args: []string{"-c", script}} }

func TestExitCode(t *testing.T) {
	res, err := Run(context.Background(), sh("exit 7"))
	if err != nil || res.ExitCode != 7 || res.Signaled {
		t.Errorf("Run = %+v, %v", res, err)
	}
}

func TestStartFailure(t *testing.T) {
	if _, err := Run(context.Background(), Cmd{Path: "/nonexistent/tern-no-such-binary"}); err == nil {
		t.Error("want an error for a missing executable")
	}
}

func TestLinesSplitOnCRAndLF(t *testing.T) {
	var out lines
	c := sh(`printf 'a\rb\nc'`)
	c.OnStdout = out.add
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if got := out.all(); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("lines = %q", got)
	}
}

func TestLongLineTruncated(t *testing.T) {
	var out lines
	c := sh(`head -c 204800 /dev/zero | tr '\0' x; echo; echo tail`)
	c.OnStdout = out.add
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	got := out.all()
	if len(got) != 2 || got[1] != "tail" {
		t.Fatalf("got %d lines, last %q", len(got), got[len(got)-1])
	}
	if len(got[0]) != MaxLine {
		t.Errorf("long line delivered as %d bytes, want %d", len(got[0]), MaxLine)
	}
}

func TestStderrTail(t *testing.T) {
	// 10 KiB of 'a' followed by a marker: the tail must be the end.
	res, err := Run(context.Background(), sh(`{ head -c 10240 /dev/zero | tr '\0' a; printf END; } >&2; exit 1`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.StderrTail) != 4096 || !strings.HasSuffix(res.StderrTail, "aaaEND") {
		t.Errorf("StderrTail is %d bytes ending %q", len(res.StderrTail), res.StderrTail[max(0, len(res.StderrTail)-6):])
	}
}

func TestEnvIsAllowlisted(t *testing.T) {
	t.Setenv("TERN_TEST_CANARY", "1")
	var out lines
	c := Cmd{Path: "/usr/bin/env", Env: []string{"X=1"}, OnStdout: out.add}
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, l := range out.all() {
		k, v, _ := strings.Cut(l, "=")
		env[k] = v
	}
	if _, leaked := env["TERN_TEST_CANARY"]; leaked {
		t.Error("child inherited TERN_TEST_CANARY")
	}
	if env["PATH"] != os.Getenv("PATH") || env["LANG"] != "C.UTF-8" || env["X"] != "1" {
		t.Errorf("env = %v", env)
	}
	for k := range env {
		switch k {
		case "PATH", "HOME", "TZ", "LANG", "X":
		default:
			t.Errorf("unexpected variable %s in child environment", k)
		}
	}
}

func TestStdin(t *testing.T) {
	var out lines
	c := Cmd{Path: "/bin/cat", Stdin: strings.NewReader("hello"), OnStdout: out.add}
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if got := out.all(); !reflect.DeepEqual(got, []string{"hello"}) {
		t.Errorf("stdout = %q", got)
	}
}

func TestDir(t *testing.T) {
	dir := t.TempDir()
	var out lines
	c := sh("pwd -P")
	c.Dir, c.OnStdout = dir, out.add
	Run(context.Background(), c)
	want, _ := os.Readlink(dir)
	if got := out.all(); len(got) != 1 || (got[0] != dir && got[0] != want && !strings.HasSuffix(got[0], dir)) {
		t.Errorf("pwd = %q, want %q", got, dir)
	}
}

// runCanceled starts c, cancels once the child printed its first line, and
// returns the result and how long Run took after the cancel.
func runCanceled(t *testing.T, c Cmd) (Result, []string, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out lines
	ready := make(chan struct{})
	var once sync.Once
	c.OnStdout = func(line []byte) {
		out.add(line)
		once.Do(func() { close(ready) })
	}
	type done struct {
		res Result
		err error
	}
	ch := make(chan done, 1)
	go func() {
		res, err := Run(ctx, c)
		ch <- done{res, err}
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("child never printed its ready line")
	}
	start := time.Now()
	cancel()
	select {
	case d := <-ch:
		if d.err != nil {
			t.Fatalf("Run: %v", d.err)
		}
		return d.res, out.all(), time.Since(start)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	panic("unreachable")
}

func TestCancelGraceful(t *testing.T) {
	old := KillGrace
	KillGrace = 30 * time.Second // returning quickly must be the child's doing
	defer func() { KillGrace = old }()
	res, _, took := runCanceled(t, sh(`trap 'exit 0' TERM; echo ready; while :; do sleep 0.05; done`))
	if took > 3*time.Second {
		t.Errorf("took %v: waited for the grace period", took)
	}
	if !res.Signaled || res.ExitCode != 0 {
		t.Errorf("result = %+v, want Signaled with the child's own exit code 0", res)
	}
}

func TestCancelTermThenKill(t *testing.T) {
	res, _, took := runCanceled(t, sh(`trap '' TERM; echo ready; sleep 60`))
	if took < 150*time.Millisecond || took > 3*time.Second {
		t.Errorf("took %v, want about the 200ms grace period", took)
	}
	if !res.Signaled {
		t.Errorf("result = %+v, want Signaled", res)
	}
}

func TestKillsWholeProcessGroup(t *testing.T) {
	_, out, _ := runCanceled(t, sh(`sleep 60 & echo $!; wait`))
	pid, err := strconv.Atoi(out[0])
	if err != nil {
		t.Fatalf("first line %q is not a pid", out[0])
	}
	// The grandchild is reparented and reaped by init, so give that a moment.
	deadline := time.Now().Add(5 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived the cancel", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// alive reports whether pid is a running process. A zombie counts as dead:
// in a container the orphan's new parent may be a PID 1 that never reaps.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true // no procfs (macOS): the signal probe is all there is
	}
	i := bytes.LastIndexByte(stat, ')')
	return i < 0 || i+2 >= len(stat) || stat[i+2] != 'Z'
}

func TestCallbacksOptional(t *testing.T) {
	res, err := Run(context.Background(), sh(`echo out; echo err >&2`))
	if err != nil || res.ExitCode != 0 || res.StderrTail != "err\n" {
		t.Errorf("Run = %+v, %v", res, err)
	}
}

func TestBaseEnv(t *testing.T) {
	t.Setenv("TZ", "Asia/Shanghai")
	env := BaseEnv()
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range []string{"\nPATH=" + os.Getenv("PATH") + "\n", "\nTZ=Asia/Shanghai\n", "\nLANG=C.UTF-8\n", "\nHOME="} {
		if !bytes.Contains([]byte(joined), []byte(want)) {
			t.Errorf("BaseEnv() = %q, missing %q", env, want)
		}
	}
	if len(env) != 4 {
		t.Errorf("BaseEnv() has %d entries, want 4", len(env))
	}
}
