//go:build integration

package integration

import (
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tern/internal/model"
	kit "tern/test/testkit"
)

type listing struct {
	Path    string `json:"path"`
	Missing bool   `json:"missing"`
	Entries []struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		Mount bool   `json:"mount"`
	} `json:"entries"`
}

func (l listing) names() []string {
	var out []string
	for _, e := range l.Entries {
		out = append(out, e.Name)
	}
	return out
}

func browse(t *testing.T, a *kit.App, base, p string) listing {
	t.Helper()
	var l listing
	a.Must(http.StatusOK, "GET", base+"?path="+url.QueryEscape(p), nil, &l)
	return l
}

// TestBrowseLocal: mount points stand out, so the volumes of a container
// can be told from its own folders.
func TestBrowseLocal(t *testing.T) {
	a := kit.StartApp(t)
	l := browse(t, a, "/api/v1/browse", "/dev")
	for _, e := range l.Entries {
		if e.Name == "shm" {
			if !e.Mount {
				t.Errorf("/dev/shm is not marked as a mount point")
			}
			return
		}
	}
	t.Errorf("/dev lists %v, no shm", l.names())
}

// TestBrowseRemote lists folders on the real SSH and WebDAV servers, through
// the bundled rclone with the configuration a sync run gets.
func TestBrowseRemote(t *testing.T) {
	a := kit.StartApp(t)
	for _, protocol := range []string{"sftp", "webdav"} {
		t.Run(protocol, func(t *testing.T) {
			conn := a.ConnFor(t, protocol)
			dir := kit.NewRemoteDir(t, protocol)
			for _, d := range []string{"b", "a/x", "照片 2024"} {
				if err := os.MkdirAll(filepath.Join(dir.FS, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			kit.WriteTree(t, dir.FS, map[string]string{"file.txt": "not a folder"})
			base := "/api/v1/connections/" + conn + "/browse"

			l := browse(t, a, base, dir.Task)
			if want := []string{"a", "b", "照片 2024"}; !reflect.DeepEqual(l.names(), want) || l.Path != dir.Task || l.Missing {
				t.Errorf("%s: path %q, missing %v, names %q; want %q", dir.Task, l.Path, l.Missing, l.names(), want)
			}
			if len(l.Entries) == 3 && l.Entries[2].Path != path.Join(dir.Task, "照片 2024") {
				t.Errorf("entry path = %q", l.Entries[2].Path)
			}

			l = browse(t, a, base, dir.Task+"/a/new/deeper")
			if l.Path != dir.Task+"/a" || !l.Missing || !reflect.DeepEqual(l.names(), []string{"x"}) {
				t.Errorf("missing folder: path %q, missing %v, names %q", l.Path, l.Missing, l.names())
			}

			// The starting point: the SSH user's home, the WebDAV root.
			l = browse(t, a, base, "")
			if l.Path != "" {
				t.Errorf("starting point listed as %q", l.Path)
			}
		})
	}
}

// TestBrowseSnapshot lists a real snapshot and restores the folder it
// offers: the picked path is one restore takes.
func TestBrowseSnapshot(t *testing.T) {
	a := kit.StartApp(t)
	conn := a.SSHConnection(t)
	src := filepath.Join(t.TempDir(), "src")
	kit.WriteTree(t, src, map[string]string{"docs/a.txt": "a\n", "docs/2024/b.txt": "b\n", "photos/c.jpg": "c"})
	task, _ := a.NewBackupTask(t, conn, []string{src}, kit.BackupOpts{})
	a.WantRun(t, a.RunAndWait(t, task, nil), model.StatusSuccess)
	var snaps struct {
		Snapshots []struct {
			ID string `json:"id"`
		} `json:"snapshots"`
	}
	a.Must(http.StatusOK, "GET", "/api/v1/tasks/"+task+"/snapshots", nil, &snaps)
	if len(snaps.Snapshots) != 1 {
		t.Fatalf("%d snapshots", len(snaps.Snapshots))
	}
	snap := snaps.Snapshots[0].ID
	base := "/api/v1/tasks/" + task + "/snapshots/" + snap + "/browse"

	l := browse(t, a, base, src)
	if !reflect.DeepEqual(l.names(), []string{"docs", "photos"}) || l.Missing {
		t.Errorf("%s: names %q, missing %v", src, l.names(), l.Missing)
	}
	l = browse(t, a, base, src+"/docs/nope")
	if l.Path != src+"/docs" || !l.Missing || !reflect.DeepEqual(l.names(), []string{"2024"}) {
		t.Errorf("missing folder: path %q, missing %v, names %q", l.Path, l.Missing, l.names())
	}
	l = browse(t, a, base, "")
	if first := strings.Split(src, "/")[1]; l.Path != "/" || !reflect.DeepEqual(l.names(), []string{first}) {
		t.Errorf("root: path %q, names %q; want [%s]", l.Path, l.names(), first)
	}

	target := filepath.Join(kit.MountedDir(t), "restored")
	var started struct {
		RunID string `json:"run_id"`
	}
	a.Must(http.StatusAccepted, "POST", "/api/v1/tasks/"+task+"/restore",
		map[string]any{"snapshot_id": snap, "subpath": src + "/docs", "target_dir": target}, &started)
	a.WantRun(t, a.WaitRun(t, started.RunID), model.StatusSuccess)
	if got, want := kit.FileSet(t, target), []string{"2024/b.txt", "a.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("restored %q, want %q", got, want)
	}
}
