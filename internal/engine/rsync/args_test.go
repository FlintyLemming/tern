package rsync

import (
	"reflect"
	"slices"
	"testing"

	"tern/internal/model"
)

const cfg = "/run/x/ssh_config"

var sshArg = `ssh -F "/run/x/ssh_config"`

func spec(direction, mode string) model.SyncSpec {
	return model.SyncSpec{Protocol: "rsync", ConnectionID: "c1", Direction: direction, DeleteMode: mode,
		MaxDeletes: 100, Compare: "size_mtime"}
}

var pairA = model.FolderPair{Local: "/sources/a", Remote: "/srv/a"}

func TestArgsPushMirror(t *testing.T) {
	s := spec("push", "mirror")
	s.LimitKiB = 2048
	s.Excludes = []string{"*.tmp"}
	got := Args(s, pairA, cfg, "F", false, 7)
	want := []string{"-aHs", "--no-inc-recursive", "--partial-dir=.tern-partial", "--timeout=60", "--info=progress2", "--stats",
		"--out-format=%i %n%L", "-e", sshArg, `--rsync-path=sh -c 'echo tern-pid:$$ >&2; exec rsync "$@"' rsync`,
		"--delete", "--max-delete=7", "--bwlimit=2048", "--exclude-from=F",
		"/sources/a/", "tern-target:/srv/a/"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Args =\n%q\nwant\n%q", got, want)
	}
}

func TestArgsPullKeep(t *testing.T) {
	got := Args(spec("pull", "keep"), model.FolderPair{Local: "/targets/a", Remote: "/srv/a"}, cfg, "", false, -1)
	if tail := got[len(got)-2:]; !reflect.DeepEqual(tail, []string{"tern-target:/srv/a/", "/targets/a/"}) {
		t.Errorf("source and destination = %q", tail)
	}
	if slices.Contains(got, "--delete") {
		t.Errorf("Args = %q", got)
	}
}

func TestArgsKeepNeverDeletes(t *testing.T) {
	for _, maxDelete := range []int{-1, 0, 7, 1000} {
		for _, a := range Args(spec("push", "keep"), pairA, cfg, "", false, maxDelete) {
			if a == "--delete" || len(a) > 12 && a[:12] == "--max-delete" {
				t.Errorf("maxDelete %d: Args has %q", maxDelete, a)
			}
		}
	}
}

// A mirror dry run must list deletions, so --delete does not depend on a
// limit being passed; the limit itself only comes with maxDelete >= 0.
func TestArgsMirrorDryRunListsDeletes(t *testing.T) {
	got := Args(spec("push", "mirror"), pairA, cfg, "", true, -1)
	if !slices.Contains(got, "--delete") || !slices.Contains(got, "--dry-run") {
		t.Errorf("Args = %q", got)
	}
	for _, a := range got {
		if len(a) > 12 && a[:12] == "--max-delete" {
			t.Errorf("Args has %q", a)
		}
	}
}

func TestArgsDryRun(t *testing.T) {
	if got := Args(spec("push", "keep"), pairA, cfg, "", true, -1); !slices.Contains(got, "--dry-run") {
		t.Errorf("Args = %q", got)
	}
	if got := Args(spec("push", "keep"), pairA, cfg, "", false, -1); slices.Contains(got, "--dry-run") {
		t.Errorf("Args = %q", got)
	}
}

func TestArgsChecksum(t *testing.T) {
	s := spec("push", "keep")
	s.Compare = "checksum"
	if got := Args(s, pairA, cfg, "", false, -1); !slices.Contains(got, "--checksum") {
		t.Errorf("Args = %q", got)
	}
	if got := Args(spec("push", "keep"), pairA, cfg, "", false, -1); slices.Contains(got, "--checksum") {
		t.Errorf("size_mtime Args = %q", got)
	}
}

func TestArgsTrailingSlashes(t *testing.T) {
	got := Args(spec("push", "keep"), model.FolderPair{Local: "/sources/a/", Remote: "/srv/a/"}, cfg, "", false, -1)
	if tail := got[len(got)-2:]; !reflect.DeepEqual(tail, []string{"/sources/a/", "tern-target:/srv/a/"}) {
		t.Errorf("source and destination = %q", tail)
	}
}

func TestExitKind(t *testing.T) {
	for _, c := range []struct {
		code    int
		tail    string
		kind    model.ErrKind
		warning bool
	}{
		{0, "", "", false},
		{24, "", "", true},
		{23, "", model.KindData, false},
		{25, "", model.KindGuard, false},
		{1, "", model.KindConfig, false},
		{12, "", model.KindNetwork, false},
		{30, "", model.KindNetwork, false},
		{255, "", model.KindNetwork, false},
		{255, "Host key verification failed.", model.KindHostKey, false},
		{255, "Permission denied (publickey).", model.KindAuth, false},
		{12, "bash: line 1: rsync: command not found", model.KindConfig, false},
		{99, "", model.KindUnknown, false},
	} {
		kind, warning := ExitKind(c.code, c.tail)
		if kind != c.kind || warning != c.warning {
			t.Errorf("ExitKind(%d, %q) = %q, %v; want %q, %v", c.code, c.tail, kind, warning, c.kind, c.warning)
		}
	}
}
