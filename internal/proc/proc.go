// Package proc runs the child processes tern delegates to: restic, rclone,
// rsync, ssh and hook shells. Children get an allowlisted environment and
// their own process group, so stopping one also stops whatever it started.
package proc

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const (
	MaxLine    = 64 << 10 // longer output lines are cut to this
	stderrTail = 4 << 10
)

// KillGrace is how long a child gets between SIGTERM and SIGKILL.
var KillGrace = 30 * time.Second

// Cmd describes one child process.
type Cmd struct {
	Path  string
	Args  []string
	Env   []string // appended to BaseEnv()
	Dir   string
	Stdin io.Reader
	// Called once per output line, without the terminator. Lines end at \n
	// or \r (progress displays redraw with \r); empty lines are skipped and
	// lines longer than MaxLine are cut. The two callbacks run on different
	// goroutines.
	OnStdout, OnStderr func(line []byte)
}

type Result struct {
	ExitCode   int    // -1 when the child was killed by a signal
	StderrTail string // the last 4 KiB of stderr
	Signaled   bool   // this package terminated the child because ctx ended
}

// BaseEnv is the environment every child starts from: PATH, HOME and TZ as
// tern has them, and a fixed UTF-8 locale. Nothing else of tern's own
// environment is inherited.
func BaseEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "TZ"} {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			env = append(env, k+"="+v)
		}
	}
	return append(env, "LANG=C.UTF-8")
}

// splitter turns a byte stream into capped lines.
type splitter struct {
	emit func([]byte)
	buf  []byte
	over bool // the current line already hit MaxLine; drop until it ends
}

func (s *splitter) feed(p []byte) {
	for _, b := range p {
		if b == '\n' || b == '\r' {
			s.flush()
			continue
		}
		if s.over {
			continue
		}
		s.buf = append(s.buf, b)
		if len(s.buf) == MaxLine {
			s.emit(s.buf)
			s.buf, s.over = s.buf[:0], true
		}
	}
}

func (s *splitter) flush() {
	if len(s.buf) > 0 {
		s.emit(s.buf)
	}
	s.buf, s.over = s.buf[:0], false
}

// pump reads r to the end, feeding lines to on and, when tail is non-nil,
// keeping the last stderrTail bytes there.
func pump(r io.Reader, on func([]byte), tail *[]byte) {
	if on == nil {
		on = func([]byte) {}
	}
	sp := splitter{emit: on}
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			sp.feed(buf[:n])
			if tail != nil {
				*tail = append(*tail, buf[:n]...)
				if extra := len(*tail) - stderrTail; extra > 0 {
					*tail = append((*tail)[:0], (*tail)[extra:]...)
				}
			}
		}
		if err != nil {
			break
		}
	}
	sp.flush()
}

// Run starts c and waits for it. The error only says the process could not
// be started; a non-zero exit is reported in Result. When ctx ends, the
// child's whole process group gets SIGTERM and, KillGrace later, SIGKILL.
func Run(ctx context.Context, c Cmd) (Result, error) {
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Env = append(BaseEnv(), c.Env...)
	cmd.Dir = c.Dir
	cmd.Stdin = c.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, err
	}
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}
	pgid := cmd.Process.Pid

	var res Result
	finished := make(chan struct{})
	watcher := make(chan struct{})
	go func() {
		defer close(watcher)
		select {
		case <-finished:
			return
		case <-ctx.Done():
		}
		res.Signaled = true
		syscall.Kill(-pgid, syscall.SIGTERM)
		grace := time.NewTimer(KillGrace)
		defer grace.Stop()
		select {
		case <-finished:
		case <-grace.C:
			syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}()

	// The pipes close when every process holding them has exited, so this
	// also waits for grandchildren that kept stdout or stderr.
	var tail []byte
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pump(stdout, c.OnStdout, nil) }()
	go func() { defer wg.Done(); pump(stderr, c.OnStderr, &tail) }()
	wg.Wait()
	werr := cmd.Wait()
	close(finished)
	<-watcher

	res.StderrTail = string(tail)
	res.ExitCode = cmd.ProcessState.ExitCode()
	var exitErr *exec.ExitError
	if werr != nil && !errors.As(werr, &exitErr) && res.ExitCode == 0 {
		res.ExitCode = -1 // Wait itself failed (for example copying stdin)
	}
	return res, nil
}
