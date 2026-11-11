// Package restictest provides a stand-in for the restic executable: a shell
// script that records how it was called and plays back scripted output.
// Unit tests of everything that drives restic use it; the real tool only
// appears in the integration tests.
package restictest

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Step is what the fake does on one call: print, then exit.
type Step struct {
	Stdout string
	Stderr string
	Exit   int
}

// Call is one recorded invocation.
type Call struct {
	Args []string
	Env  map[string]string // the RESTIC_* variables
	// Password is what RESTIC_PASSWORD_FILE held when the call was made;
	// PasswordMode is that file's permission bits.
	Password     string
	PasswordMode os.FileMode
}

// Sub is the restic subcommand of the call: the first argument after the
// global options ("-r repo -o x -o y").
func (c Call) Sub() string {
	for i := 0; i < len(c.Args); i++ {
		if c.Args[i] == "-r" || c.Args[i] == "-o" {
			i++
			continue
		}
		return c.Args[i]
	}
	return ""
}

// Has reports whether the arguments contain want as a consecutive run.
func (c Call) Has(want ...string) bool {
	for i := 0; i+len(want) <= len(c.Args); i++ {
		match := true
		for j, w := range want {
			if c.Args[i+j] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// Fake is one fake restic executable.
type Fake struct {
	Bin string
	t   testing.TB
	dir string
}

const script = `#!/bin/sh
d='%s'
n=$(cat "$d/count")
echo $((n+1)) > "$d/count"
c="$d/call-$n"
: > "$c"
for a in "$@"; do printf 'arg %%s\n' "$a" >> "$c"; done
env | grep '^RESTIC_' | sed 's/^/env /' >> "$c"
if [ -n "$RESTIC_PASSWORD_FILE" ] && [ -f "$RESTIC_PASSWORD_FILE" ]; then
  cp -p "$RESTIC_PASSWORD_FILE" "$c.pw"
fi
if [ -f "$d/step-$n.out" ]; then cat "$d/step-$n.out"; fi
if [ -f "$d/step-$n.err" ]; then cat "$d/step-$n.err" >&2; fi
if [ -f "$d/step-$n.exit" ]; then exit "$(cat "$d/step-$n.exit")"; fi
exit 0
`

// New writes a fake restic that plays steps in order, one per call. Calls
// beyond the last step print nothing and exit 0.
func New(t testing.TB, steps ...Step) *Fake {
	t.Helper()
	dir := t.TempDir()
	if strings.Contains(dir, "'") {
		t.Fatalf("temporary directory %q cannot be quoted in a shell script", dir)
	}
	f := &Fake{Bin: filepath.Join(dir, "restic"), t: t, dir: dir}
	f.write("count", "0")
	if err := os.WriteFile(f.Bin, []byte(fmt.Sprintf(script, dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	f.Add(steps...)
	return f
}

func (f *Fake) write(name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// Add scripts the next calls: steps go after those already scripted, and
// after the calls already made.
func (f *Fake) Add(steps ...Step) {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "count"))
	if err != nil {
		f.t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		f.t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(f.dir, fmt.Sprintf("step-%d.exit", n))); err != nil {
			break
		}
		n++
	}
	for i, s := range steps {
		p := fmt.Sprintf("step-%d", n+i)
		f.write(p+".out", s.Stdout)
		f.write(p+".err", s.Stderr)
		f.write(p+".exit", strconv.Itoa(s.Exit))
	}
}

// Calls returns every invocation so far, in order.
func (f *Fake) Calls() []Call {
	f.t.Helper()
	var calls []Call
	for n := 0; ; n++ {
		path := filepath.Join(f.dir, fmt.Sprintf("call-%d", n))
		raw, err := os.ReadFile(path)
		if err != nil {
			return calls
		}
		c := Call{Env: map[string]string{}}
		for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
			switch {
			case strings.HasPrefix(line, "arg "):
				c.Args = append(c.Args, strings.TrimPrefix(line, "arg "))
			case strings.HasPrefix(line, "env "):
				k, v, _ := strings.Cut(strings.TrimPrefix(line, "env "), "=")
				c.Env[k] = v
			}
		}
		if pw, err := os.ReadFile(path + ".pw"); err == nil {
			c.Password = string(pw)
			if fi, err := os.Stat(path + ".pw"); err == nil {
				c.PasswordMode = fi.Mode().Perm()
			}
		}
		calls = append(calls, c)
	}
}

// Subs lists the subcommands called so far.
func (f *Fake) Subs() []string {
	f.t.Helper()
	var subs []string
	for _, c := range f.Calls() {
		subs = append(subs, c.Sub())
	}
	return subs
}
