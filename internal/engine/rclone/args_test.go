package rclone

import (
	"reflect"
	"slices"
	"testing"

	"tern/internal/model"
)

var fixedArgs = []string{"--config", "/run/x/rclone.conf", "--use-json-log", "--log-level", "INFO", "--stats", "2s",
	"--timeout", "60s", "--contimeout", "15s", "--retries", "1", "--low-level-retries", "3", "--skip-links", "--create-empty-src-dirs"}

func spec(direction, mode string) model.SyncSpec {
	return model.SyncSpec{Protocol: "sftp", ConnectionID: "c1", Direction: direction, DeleteMode: mode,
		MaxDeletes: 100, Compare: "size_mtime", Transfers: 4}
}

var pairA = model.FolderPair{Local: "/sources/a", Remote: "/srv/a"}

func join(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestArgsPushKeep(t *testing.T) {
	s := spec("push", "keep")
	s.LimitKiB = 2048
	s.Excludes = []string{"*.tmp"}
	got := Args(s, pairA, "/run/x/rclone.conf", "/run/x/filter", false, 7)
	want := join([]string{"copy", "/sources/a", "remote:/srv/a"}, fixedArgs,
		[]string{"--transfers", "4", "--bwlimit", "2048Ki", "--filter-from", "/run/x/filter"})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Args =\n%q\nwant\n%q", got, want)
	}
}

func TestArgsPullMirror(t *testing.T) {
	got := Args(spec("pull", "mirror"), model.FolderPair{Local: "/targets/a", Remote: "/srv/a"}, "/run/x/rclone.conf", "", false, 7)
	want := join([]string{"sync", "remote:/srv/a", "/targets/a"}, fixedArgs, []string{"--transfers", "4", "--max-delete", "7"})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Args =\n%q\nwant\n%q", got, want)
	}
}

func TestArgsDryRun(t *testing.T) {
	got := Args(spec("push", "mirror"), pairA, "/run/x/rclone.conf", "", true, -1)
	if !slices.Contains(got, "--dry-run") || slices.Contains(got, "--max-delete") {
		t.Errorf("Args = %q", got)
	}
}

func TestArgsChecksum(t *testing.T) {
	s := spec("push", "keep")
	s.Compare = "checksum"
	if got := Args(s, pairA, "/run/x/rclone.conf", "", false, -1); !slices.Contains(got, "--checksum") {
		t.Errorf("Args = %q", got)
	}
	if got := Args(spec("push", "keep"), pairA, "/run/x/rclone.conf", "", false, -1); slices.Contains(got, "--checksum") {
		t.Errorf("size_mtime Args = %q", got)
	}
}

func TestArgsMinimal(t *testing.T) {
	got := Args(spec("push", "keep"), pairA, "/run/x/rclone.conf", "", false, -1)
	if slices.Contains(got, "--bwlimit") || slices.Contains(got, "--filter-from") {
		t.Errorf("Args = %q", got)
	}
}

func TestArgsKeepNeverDeletes(t *testing.T) {
	for _, maxDelete := range []int{-1, 0, 7, 1000} {
		got := Args(spec("push", "keep"), pairA, "/run/x/rclone.conf", "", false, maxDelete)
		if got[0] != "copy" || slices.Contains(got, "--max-delete") {
			t.Errorf("maxDelete %d: Args = %q", maxDelete, got)
		}
	}
}

func TestExitKind(t *testing.T) {
	for _, c := range []struct {
		code int
		tail string
		want model.ErrKind
	}{
		{0, "", ""},
		{5, "", model.KindNetwork},
		{2, "", model.KindConfig},
		{3, "", model.KindConfig},
		{4, "", model.KindConfig},
		{6, "", model.KindData},
		{1, "connection reset by peer", model.KindNetwork},
		{1, "", model.KindUnknown},
		{7, "", model.KindUnknown},
	} {
		if got := ExitKind(c.code, c.tail); got != c.want {
			t.Errorf("ExitKind(%d, %q) = %q, want %q", c.code, c.tail, got, c.want)
		}
	}
}

// rclone's own --max-delete stopping a sync is the second gate of the
// mirror guard, not an accident worth retrying.
func TestExitKindMaxDelete(t *testing.T) {
	tail := `{"level":"error","msg":"Got fatal error on delete: --max-delete threshold reached","object":"y"}`
	if got := ExitKind(7, tail); got != model.KindGuard {
		t.Errorf("ExitKind = %q, want guard", got)
	}
}
