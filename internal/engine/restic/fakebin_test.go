package restic

import (
	"testing"

	"tern/internal/engine/restic/restictest"
	"tern/internal/rundir"
)

type (
	fakeStep = restictest.Step
	fakeCall = restictest.Call
)

// fakeRestic writes a fake restic executable that plays steps in order and
// returns its path and a function listing the calls made so far.
func fakeRestic(t *testing.T, steps ...fakeStep) (bin string, calls func() []fakeCall) {
	t.Helper()
	f := restictest.New(t, steps...)
	return f.Bin, f.Calls
}

func subs(calls []fakeCall) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c.Sub())
	}
	return out
}

func newRunDir(t *testing.T) *rundir.Dir {
	t.Helper()
	dir, err := rundir.New(t.TempDir(), "run1")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
