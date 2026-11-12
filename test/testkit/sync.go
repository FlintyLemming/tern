//go:build integration || acceptance

package testkit

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"testing"

	"tern/internal/model"
)

// SyncOpts are the parts of a sync task the tests vary.
type SyncOpts struct {
	Protocol  string
	Direction string // default push
	Mode      string // default keep
	Pairs     []model.FolderPair
	Spec      map[string]any
	Task      map[string]any
}

// NewSyncTask creates a manual sync task and returns its ID. Runs are not
// retried unless o.Task says otherwise, so that a failure shows at once.
func (a *App) NewSyncTask(t *testing.T, connID string, o SyncOpts) string {
	t.Helper()
	if o.Direction == "" {
		o.Direction = "push"
	}
	if o.Mode == "" {
		o.Mode = "keep"
	}
	var pairs []map[string]any
	for _, p := range o.Pairs {
		pairs = append(pairs, map[string]any{"local": p.Local, "remote": p.Remote})
	}
	spec := map[string]any{"protocol": o.Protocol, "connection_id": connID, "direction": o.Direction, "delete_mode": o.Mode, "pairs": pairs}
	for k, v := range o.Spec {
		spec[k] = v
	}
	body := map[string]any{"type": "sync", "name": "sync-" + model.NewID(), "enabled": true, "sync": spec,
		"retry": map[string]any{"retries": 0, "delays_sec": []int{1}}}
	for k, v := range o.Task {
		body[k] = v
	}
	var created map[string]any
	a.Must(http.StatusCreated, "POST", "/api/v1/tasks", body, &created)
	return created["id"].(string)
}

// ConnFor makes a connection of the kind protocol needs.
func (a *App) ConnFor(t *testing.T, protocol string) string {
	t.Helper()
	if protocol == "webdav" {
		return a.WebDAVConnection(t)
	}
	return a.SSHConnection(t)
}

// RemoteDir is a directory on the remote end: the path a task names, and
// where the runner sees it.
type RemoteDir struct{ Task, FS string }

func (d RemoteDir) Sub(name string) RemoteDir {
	return RemoteDir{Task: path.Join(d.Task, name), FS: filepath.Join(d.FS, name)}
}

// NewRemoteDir makes a fresh directory on the remote end of protocol. The
// WebDAV server serves /srv/dav of the same volume as the SSH server.
func NewRemoteDir(t *testing.T, protocol string) RemoteDir {
	t.Helper()
	if protocol == "webdav" {
		name := "t-" + model.NewID()
		dir := filepath.Join("/remote/dav", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return RemoteDir{Task: "/" + name, FS: dir}
	}
	p := RemotePath(t)
	return RemoteDir{Task: p, FS: RemoteFS(t, p)}
}

// FileSet lists the regular files under root, relative and "/"-separated.
// A root that does not exist holds none.
func FileSet(t *testing.T, root string) []string {
	t.Helper()
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(root, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(files)
	return files
}
