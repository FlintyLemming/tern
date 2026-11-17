package api

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tern/internal/config"
	"tern/internal/engine/faketool"
	"tern/internal/engine/restic/restictest"
)

func browseQuery(base, p string) string { return base + "?path=" + url.QueryEscape(p) }

// listed returns the names and paths of a listing's entries.
func listed(t *testing.T, got map[string]any) (names, paths []string) {
	t.Helper()
	for _, e := range got["entries"].([]any) {
		m := e.(map[string]any)
		names = append(names, m["name"].(string))
		paths = append(paths, m["path"].(string))
	}
	return names, paths
}

func mkdirs(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(root, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBrowseLocal(t *testing.T) {
	h := readyHarness(t)
	root := t.TempDir()
	mkdirs(t, root, "b", "A", "c/deeper")
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for link, to := range map[string]string{"link": "b", "dead": "nowhere", "flink": "f.txt"} {
		if err := os.Symlink(to, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}

	got := wantStatus(t, h.do("GET", browseQuery("/api/v1/browse", root), nil), http.StatusOK)
	names, paths := listed(t, got)
	if want := []string{"A", "b", "c", "link"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v: folders and links to folders, by name", names, want)
	}
	if paths[3] != filepath.Join(root, "link") {
		t.Errorf("path of link = %q", paths[3])
	}
	if got["path"] != root || got["missing"] != false || got["truncated"] != false {
		t.Errorf("listing = %v", got)
	}
	for _, e := range got["entries"].([]any) {
		if e.(map[string]any)["mount"] != false {
			t.Errorf("%v is marked as a mount point", e)
		}
	}

	// A folder that is not there yet is shown as its nearest ancestor.
	got = wantStatus(t, h.do("GET", browseQuery("/api/v1/browse", filepath.Join(root, "c", "new", "deeper")), nil), http.StatusOK)
	if got["path"] != filepath.Join(root, "c") || got["missing"] != true {
		t.Errorf("missing folder: path %v, missing %v", got["path"], got["missing"])
	}
	if names, _ := listed(t, got); !reflect.DeepEqual(names, []string{"deeper"}) {
		t.Errorf("missing folder: names %v", names)
	}
	// A file is shown as the folder it is in.
	got = wantStatus(t, h.do("GET", browseQuery("/api/v1/browse", filepath.Join(root, "f.txt")), nil), http.StatusOK)
	if got["path"] != root || got["missing"] != true {
		t.Errorf("file: path %v, missing %v", got["path"], got["missing"])
	}
	// An empty folder has an empty list, not null.
	got = wantStatus(t, h.do("GET", browseQuery("/api/v1/browse", filepath.Join(root, "A")), nil), http.StatusOK)
	if e, ok := got["entries"].([]any); !ok || len(e) != 0 {
		t.Errorf("empty folder: entries = %#v", got["entries"])
	}
}

func TestBrowseLocalStart(t *testing.T) {
	h := readyHarness(t)
	got := wantStatus(t, h.do("GET", "/api/v1/browse", nil), http.StatusOK)
	if got["path"] != "/" || got["missing"] != false {
		t.Errorf("start on Docker: path %v, missing %v; want /", got["path"], got["missing"])
	}
	if localStart(config.PlatformQTS) != "/share" || localStart(config.PlatformDocker) != "/" {
		t.Errorf("localStart: qts %q, docker %q", localStart(config.PlatformQTS), localStart(config.PlatformDocker))
	}
}

func TestBrowseLocalRefusesBadPaths(t *testing.T) {
	h := readyHarness(t)
	for _, p := range []string{"relative/dir", "/a/../b", "/a/", "/a\nb"} {
		e := wantError(t, h.do("GET", browseQuery("/api/v1/browse", p), nil), http.StatusBadRequest, "validation")
		wantField(t, e, "path")
	}
}

func TestBrowseLocalUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every folder")
	}
	h := readyHarness(t)
	dir := filepath.Join(t.TempDir(), "closed")
	mkdirs(t, dir, "x")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	wantError(t, h.do("GET", browseQuery("/api/v1/browse", dir), nil), http.StatusBadRequest, "unreadable")
}

func TestBrowseLocalCapped(t *testing.T) {
	h := readyHarness(t)
	root := t.TempDir()
	for i := 0; i < maxBrowseEntries+5; i++ {
		mkdirs(t, root, fmt.Sprintf("d%04d", i))
	}
	got := wantStatus(t, h.do("GET", browseQuery("/api/v1/browse", root), nil), http.StatusOK)
	if names, _ := listed(t, got); len(names) != maxBrowseEntries || names[0] != "d0000" || got["truncated"] != true {
		t.Errorf("%d entries, first %q, truncated %v; want the first %d by name", len(names), names[0], got["truncated"], maxBrowseEntries)
	}
}

func TestBrowseNeedsUnlocked(t *testing.T) {
	h := newHarness(t)
	wantError(t, h.do("GET", "/api/v1/browse", nil), http.StatusUnauthorized, "unauthorized")
}

// lsjson is what `rclone lsjson` prints for these entries; names ending in
// "/" are folders.
func lsjson(names ...string) string {
	var parts []string
	for _, n := range names {
		dir := strings.HasSuffix(n, "/")
		n = strings.TrimSuffix(n, "/")
		parts = append(parts, fmt.Sprintf(`{"Path":%q,"Name":%q,"Size":-1,"IsDir":%v}`, n, n, dir))
	}
	return "[\n" + strings.Join(parts, ",\n") + "\n]\n"
}

func connBrowse(id string) string { return "/api/v1/connections/" + id + "/browse" }

func TestBrowseConnection(t *testing.T) {
	h := readyHarness(t)
	id := h.sshConnID()
	h.rclone.Add(faketool.Step{Stdout: lsjson("b/", "a/", "file.txt")})
	got := wantStatus(t, h.do("GET", browseQuery(connBrowse(id), "/srv/backup/"), nil), http.StatusOK)
	names, paths := listed(t, got)
	if !reflect.DeepEqual(names, []string{"a", "b"}) || !reflect.DeepEqual(paths, []string{"/srv/backup/a", "/srv/backup/b"}) {
		t.Errorf("names %v, paths %v", names, paths)
	}
	if got["path"] != "/srv/backup" || got["missing"] != false {
		t.Errorf("listing = %v", got)
	}
	calls := h.rclone.Calls()
	if len(calls) != 1 {
		t.Fatalf("%d rclone calls, want 1", len(calls))
	}
	c := calls[0]
	if c.Args[0] != "lsjson" || c.Args[1] != "remote:/srv/backup" || !c.Has("--dirs-only") {
		t.Errorf("rclone args = %q", c.Args)
	}
	i := 0
	for i < len(c.Args) && c.Args[i] != "--config" {
		i++
	}
	if i+1 >= len(c.Args) || !strings.HasPrefix(c.Args[i+1], h.runtimeDir) {
		t.Errorf("rclone args = %q, want --config inside the runtime directory %s", c.Args, h.runtimeDir)
	}
	if left, _ := filepath.Glob(filepath.Join(h.runtimeDir, "api-*")); len(left) > 0 {
		t.Errorf("left behind: %v", left)
	}
}

func TestBrowseConnectionHome(t *testing.T) {
	h := readyHarness(t)
	id := h.sshConnID()
	h.rclone.Add(faketool.Step{Stdout: lsjson("docs/")})
	got := wantStatus(t, h.do("GET", connBrowse(id), nil), http.StatusOK)
	if _, paths := listed(t, got); !reflect.DeepEqual(paths, []string{"docs"}) || got["path"] != "" {
		t.Errorf("home: path %q, entries %v; want relative paths", got["path"], paths)
	}
	if c := h.rclone.Calls()[0]; c.Args[1] != "remote:" {
		t.Errorf("rclone lists %q, want the starting point", c.Args[1])
	}
}

func TestBrowseConnectionWalksUp(t *testing.T) {
	h := readyHarness(t)
	id := h.sshConnID()
	h.rclone.Add(faketool.Step{Exit: 3}, faketool.Step{Exit: 3}, faketool.Step{Stdout: lsjson("y/")})
	got := wantStatus(t, h.do("GET", browseQuery(connBrowse(id), "x/y/z"), nil), http.StatusOK)
	if got["path"] != "x" || got["missing"] != true {
		t.Errorf("listing = %v; want x, missing", got)
	}
	var listedAt []string
	for _, c := range h.rclone.Calls() {
		listedAt = append(listedAt, c.Args[1])
	}
	if want := []string{"remote:x/y/z", "remote:x/y", "remote:x"}; !reflect.DeepEqual(listedAt, want) {
		t.Errorf("listed %v, want %v", listedAt, want)
	}

	// Nothing at all: not even the starting point.
	h.rclone.Add(faketool.Step{Exit: 3}, faketool.Step{Exit: 3})
	wantError(t, h.do("GET", browseQuery(connBrowse(id), "gone"), nil), http.StatusBadGateway, "unreachable")
}

func TestBrowseConnectionFails(t *testing.T) {
	h := readyHarness(t)
	id := h.davConnID()
	h.rclone.Add(
		faketool.Step{Stdout: "obscured-pass\n"}, // rclone obscure, for rclone.conf
		faketool.Step{Exit: 1, Stderr: `{"level":"error","msg":"couldn't list: 401 Unauthorized for ` + davPass + `"}` + "\n"},
	)
	e := wantError(t, h.do("GET", browseQuery(connBrowse(id), "photos"), nil), http.StatusBadGateway, "unreachable")
	msg := e["message"].(string)
	if !strings.Contains(msg, "401 Unauthorized") || strings.Contains(msg, davPass) {
		t.Errorf("message = %q; want rclone's reason without the password", msg)
	}
	calls := h.rclone.Calls()
	if len(calls) != 2 || calls[0].Args[0] != "obscure" || calls[0].Stdin != davPass {
		t.Errorf("calls = %+v; want the password obscured over stdin first", calls)
	}

	wantError(t, h.do("GET", connBrowse("nope"), nil), http.StatusNotFound, "not_found")
	e = wantError(t, h.do("GET", browseQuery(connBrowse(id), "a\x00b"), nil), http.StatusBadRequest, "validation")
	wantField(t, e, "path")
}

// resticLs is what `restic ls --json <id> <dir>` prints: the snapshot, then
// nodes. Folders end in "/".
func resticLs(nodes ...string) string {
	var b strings.Builder
	b.WriteString(`{"time":"2026-11-17T05:34:29Z","paths":["/mnt/docs"],"hostname":"nas","message_type":"snapshot","struct_type":"snapshot"}` + "\n")
	for _, n := range nodes {
		typ := "file"
		if strings.HasSuffix(n, "/") {
			typ, n = "dir", strings.TrimSuffix(n, "/")
		}
		fmt.Fprintf(&b, `{"name":%q,"type":%q,"path":%q,"permissions":"drwxr-xr-x","message_type":"node","struct_type":"node"}`+"\n",
			filepath.Base(n), typ, n)
	}
	return b.String()
}

const snapID = "4f1c2b3a5d6e7f80"

func snapBrowse(task map[string]any, id string) string {
	return taskPath(task) + "/snapshots/" + id + "/browse"
}

func TestBrowseSnapshot(t *testing.T) {
	h := readyHarness(t)
	task := h.newTask(backupBody("docs", h.sshConnID()))
	before := len(h.restic.Calls())
	h.restic.Add(restictest.Step{Stdout: resticLs("/mnt/", "/mnt/docs/", "/mnt/docs/b/", "/mnt/docs/a/", "/mnt/docs/f.txt")})
	got := wantStatus(t, h.do("GET", browseQuery(snapBrowse(task, snapID), "/mnt/docs"), nil), http.StatusOK)
	names, paths := listed(t, got)
	if !reflect.DeepEqual(names, []string{"a", "b"}) || !reflect.DeepEqual(paths, []string{"/mnt/docs/a", "/mnt/docs/b"}) {
		t.Errorf("names %v, paths %v", names, paths)
	}
	if got["path"] != "/mnt/docs" || got["missing"] != false {
		t.Errorf("listing = %v", got)
	}
	calls := h.restic.Calls()[before:]
	if len(calls) != 1 {
		t.Fatalf("%d restic calls, want 1", len(calls))
	}
	c := calls[0]
	if c.Sub() != "ls" || !c.Has("--no-lock") || !c.Has("--json", snapID, "/mnt/docs") || c.Password != repoPass {
		t.Errorf("restic call = %q (password %q)", c.Args, c.Password)
	}
}

func TestBrowseSnapshotWalksUp(t *testing.T) {
	h := readyHarness(t)
	task := h.newTask(backupBody("docs", h.sshConnID()))
	// Not there; then a file of that name, which is no folder either; then its parent.
	h.restic.Add(
		restictest.Step{Stdout: resticLs("/mnt/")},
		restictest.Step{Stdout: resticLs("/mnt/", "/mnt/docs")},
		restictest.Step{Stdout: resticLs("/mnt/", "/mnt/docs", "/mnt/other/")},
	)
	got := wantStatus(t, h.do("GET", browseQuery(snapBrowse(task, snapID), "/mnt/docs/x"), nil), http.StatusOK)
	if got["path"] != "/mnt" || got["missing"] != true {
		t.Errorf("listing = %v; want /mnt, missing", got)
	}
	if names, _ := listed(t, got); !reflect.DeepEqual(names, []string{"other"}) {
		t.Errorf("names = %v", names)
	}

	// The root is the default and always there.
	h.restic.Add(restictest.Step{Stdout: resticLs("/mnt/")})
	got = wantStatus(t, h.do("GET", snapBrowse(task, snapID), nil), http.StatusOK)
	if names, _ := listed(t, got); got["path"] != "/" || !reflect.DeepEqual(names, []string{"mnt"}) {
		t.Errorf("root: path %v, names %v", got["path"], names)
	}
}

func TestBrowseSnapshotRefuses(t *testing.T) {
	h := readyHarness(t)
	task := h.newTask(backupBody("docs", h.sshConnID()))

	h.restic.Add(restictest.Step{Exit: 1, Stderr: "Fatal: failed to find snapshot: no matching ID found for prefix \"" + snapID + "\"\n"})
	wantError(t, h.do("GET", snapBrowse(task, snapID), nil), http.StatusNotFound, "not_found")

	h.restic.Add(restictest.Step{Exit: 1, Stderr: "ssh: connect to host nas2 port 22: Connection refused\n"})
	wantError(t, h.do("GET", snapBrowse(task, snapID), nil), http.StatusBadGateway, "unreachable")

	e := wantError(t, h.do("GET", snapBrowse(task, "latest"), nil), http.StatusBadRequest, "validation")
	wantField(t, e, "snapshot_id")
	for _, p := range []string{"mnt/docs", "/mnt/../etc", "/mnt/"} {
		e = wantError(t, h.do("GET", browseQuery(snapBrowse(task, snapID), p), nil), http.StatusBadRequest, "validation")
		wantField(t, e, "path")
	}

	sync := h.newSyncTask("photos")
	wantError(t, h.do("GET", snapBrowse(sync, snapID), nil), http.StatusBadRequest, "validation")
	wantError(t, h.do("GET", "/api/v1/tasks/nope/snapshots/"+snapID+"/browse", nil), http.StatusNotFound, "not_found")
}
