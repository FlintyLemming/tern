// Package faketool provides stand-ins for the rclone and rsync executables:
// a shell script that records how it was called and plays back scripted
// output, one step per call. Unit tests of everything that drives those
// tools use it; the real tools only appear in the integration tests.
package faketool

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
	Args  []string
	Stdin string
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

// Fake is one fake executable.
type Fake struct {
	Bin string
	t   testing.TB
	dir string
}

// Arguments are stored NUL-separated: file names may contain newlines.
const script = `#!/bin/sh
d='%s'
n=$(cat "$d/count")
echo $((n+1)) > "$d/count"
c="$d/call-$n"
for a in "$@"; do printf '%%s\000' "$a"; done > "$c.args"
cat > "$c.stdin"
if [ -f "$d/step-$n.out" ]; then cat "$d/step-$n.out"; fi
if [ -f "$d/step-$n.err" ]; then cat "$d/step-$n.err" >&2; fi
if [ -f "$d/step-$n.exit" ]; then exit "$(cat "$d/step-$n.exit")"; fi
exit 0
`

// New writes a fake executable called name that plays steps in order, one
// per call. Calls beyond the last step print nothing and exit 0.
func New(t testing.TB, name string, steps ...Step) *Fake {
	t.Helper()
	dir := t.TempDir()
	if strings.Contains(dir, "'") {
		t.Fatalf("temporary directory %q cannot be quoted in a shell script", dir)
	}
	f := &Fake{Bin: filepath.Join(dir, name), t: t, dir: dir}
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
		raw, err := os.ReadFile(path + ".args")
		if err != nil {
			return calls
		}
		var c Call
		if len(raw) > 0 {
			c.Args = strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
		}
		stdin, _ := os.ReadFile(path + ".stdin")
		c.Stdin = string(stdin)
		calls = append(calls, c)
	}
}
