package api

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"tern/internal/config"
	"tern/internal/engine/rclone"
	"tern/internal/engine/restic"
	"tern/internal/model"
	"tern/internal/mounts"
	"tern/internal/redact"
	"tern/internal/rundir"
	"tern/internal/store"
	"tern/internal/vault"
)

// maxBrowseEntries is how many folders one listing returns.
const maxBrowseEntries = 1000

// browseTimeout bounds one remote or snapshot listing, walking up included.
const browseTimeout = 45 * time.Second

func (a *API) registerBrowse() {
	g := []Guard{NeedSession, NeedUnlocked}
	a.Handle("GET /api/v1/browse", a.handleBrowseLocal, g...)
	a.Handle("GET /api/v1/connections/{id}/browse", a.handleBrowseConnection, g...)
	a.Handle("GET /api/v1/tasks/{id}/snapshots/{snapshot}/browse", a.handleBrowseSnapshot, g...)
}

type browseEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Mount bool   `json:"mount"` // a mount point: another device than the folder listed
}

// browseJSON is one folder's subfolders. Path is the folder listed: when
// the one asked for does not exist (Missing), its nearest existing
// ancestor.
type browseJSON struct {
	Path      string        `json:"path"`
	Missing   bool          `json:"missing"`
	Truncated bool          `json:"truncated"`
	Entries   []browseEntry `json:"entries"`
}

func badControl(s string) bool {
	return !utf8.ValidString(s) || strings.IndexFunc(s, unicode.IsControl) >= 0
}

func badBrowsePath(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusBadRequest, "validation", msg, map[string]string{"path": "invalid"})
}

// writeListing sorts entries by name, caps them and answers.
func writeListing(w http.ResponseWriter, dir string, missing bool, entries []browseEntry) {
	sort.Slice(entries, func(i, j int) bool {
		a, b := strings.ToLower(entries[i].Name), strings.ToLower(entries[j].Name)
		if a != b {
			return a < b
		}
		return entries[i].Name < entries[j].Name
	})
	out := browseJSON{Path: dir, Missing: missing, Entries: entries}
	if len(entries) > maxBrowseEntries {
		out.Entries, out.Truncated = entries[:maxBrowseEntries], true
	}
	if out.Entries == nil {
		out.Entries = []browseEntry{}
	}
	writeJSON(w, http.StatusOK, out)
}

// localStart is where browsing tern's own file system begins: the shared
// folders on a NAS, the container's root elsewhere.
func localStart(platform string) string {
	if platform == config.PlatformQTS {
		return "/share"
	}
	return "/"
}

func unreadable(w http.ResponseWriter, dir string) {
	writeError(w, http.StatusBadRequest, "unreadable", "the folder "+dir+" cannot be read", nil)
}

// handleBrowseLocal lists the folders in a directory of tern's own file
// system, the one backup sources, local sync folders and restore targets
// live on.
func (a *API) handleBrowseLocal(w http.ResponseWriter, r *http.Request) {
	want := r.URL.Query().Get("path")
	if want == "" {
		want = localStart(a.d.Cfg.Platform)
	}
	if badControl(want) || !filepath.IsAbs(want) || filepath.Clean(want) != want {
		badBrowsePath(w, "the path must be absolute and clean, like /data/photos")
		return
	}
	dir, err := mounts.NearestExisting(want)
	if err != nil {
		unreadable(w, want)
		return
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		unreadable(w, dir)
		return
	}
	if !dirInfo.IsDir() { // a file: list the folder it is in
		dir = filepath.Dir(dir)
		if dirInfo, err = os.Stat(dir); err != nil {
			unreadable(w, dir)
			return
		}
	}
	list, err := os.ReadDir(dir)
	if err != nil {
		unreadable(w, dir)
		return
	}
	dirDev, devOK := mounts.Device(dirInfo)
	var entries []browseEntry
	for _, e := range list {
		name := e.Name()
		if badControl(name) { // could not be stored as a path anyway
			continue
		}
		full := filepath.Join(dir, name)
		var info fs.FileInfo
		switch {
		case e.IsDir():
			info, err = e.Info()
		case e.Type()&fs.ModeSymlink != 0:
			info, err = os.Stat(full) // a link counts as what it points to
			if err == nil && !info.IsDir() {
				continue
			}
			info = nil // a link is not a mount point
		default:
			continue
		}
		if err != nil {
			continue // gone since ReadDir, or a dangling link
		}
		entry := browseEntry{Name: name, Path: full}
		if info != nil && devOK {
			dev, ok := mounts.Device(info)
			entry.Mount = ok && dev != dirDev
		}
		entries = append(entries, entry)
	}
	writeListing(w, dir, dir != want, entries)
}

// remoteJoin is the path of name inside dir on a remote end, in the form
// tern stores remote paths: relative to the starting point unless absolute.
func remoteJoin(dir, name string) string {
	switch dir {
	case "":
		return name
	case "/":
		return "/" + name
	}
	return dir + "/" + name
}

// remoteParent is the folder dir is in; false for a starting point.
func remoteParent(dir string) (string, bool) {
	if dir == "" || dir == "/" {
		return "", false
	}
	p := path.Dir(dir)
	if p == "." {
		p = ""
	}
	return p, true
}

// cleanRemote tidies a remote path without changing what it names: "" and
// "." are the starting point.
func cleanRemote(p string) string {
	if p == "" {
		return ""
	}
	p = path.Clean(p)
	if p == "." {
		return ""
	}
	return p
}

// handleBrowseConnection lists the folders in a directory at the remote end
// of a stored connection, through rclone with the same configuration a sync
// run gets. A directory that does not exist is answered with its nearest
// existing ancestor.
func (a *API) handleBrowseConnection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	want := r.URL.Query().Get("path")
	if badControl(want) {
		badBrowsePath(w, "the path must not contain control characters")
		return
	}
	want = cleanRemote(want)
	if _, err := a.d.Store.GetConnection(r.Context(), id); err != nil {
		storeError(w, r, err, "connection")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), browseTimeout)
	defer cancel()
	dir, err := rundir.New(a.d.Cfg.RuntimeDir, "api-"+model.NewID())
	if err != nil {
		internalError(w, r, err)
		return
	}
	defer dir.Close()
	red := redact.New()
	acc, release, err := a.d.Access.Open(ctx, id, dir, red)
	if err != nil {
		switch {
		case errors.Is(err, vault.ErrLocked):
			writeError(w, http.StatusServiceUnavailable, "vault_locked", "the credential vault is locked; unlock it with the passphrase", nil)
		case errors.Is(err, store.ErrNotFound):
			storeError(w, r, err, "connection")
		default:
			internalError(w, r, err)
		}
		return
	}
	defer release()
	conf, err := rclone.WriteConfig(ctx, dir, acc, a.d.RcloneBin)
	if err != nil {
		if !unreachableError(w, err) {
			internalError(w, r, err)
		}
		return
	}
	cur := want
	for {
		names, found, err := rclone.ListDirs(ctx, a.d.RcloneBin, conf, cur, red)
		if err != nil {
			if !unreachableError(w, err) {
				internalError(w, r, err)
			}
			return
		}
		if found {
			entries := make([]browseEntry, 0, len(names))
			for _, n := range names {
				if !badControl(n) {
					entries = append(entries, browseEntry{Name: n, Path: remoteJoin(cur, n)})
				}
			}
			writeListing(w, cur, cur != want, entries)
			return
		}
		parent, ok := remoteParent(cur)
		if !ok {
			writeError(w, http.StatusBadGateway, "unreachable", "the starting folder of the connection does not exist on the remote end", nil)
			return
		}
		cur = parent
	}
}

// unreachableError answers a remote end that failed to list. On false err
// was something else and nothing has been written.
func unreachableError(w http.ResponseWriter, err error) bool {
	var ke *model.KindError
	if !errors.As(err, &ke) {
		return false
	}
	writeError(w, http.StatusBadGateway, "unreachable", ke.Error(), nil)
	return true
}

// handleBrowseSnapshot lists the folders in a directory of one snapshot in
// a backup task's repository: what a restore can be limited to.
func (a *API) handleBrowseSnapshot(w http.ResponseWriter, r *http.Request) {
	snapID := r.PathValue("snapshot")
	want := r.URL.Query().Get("path")
	if want == "" {
		want = "/"
	}
	t, ok := a.backupTask(w, r)
	if !ok {
		return
	}
	if !restic.ValidSnapshotID(snapID) {
		writeError(w, http.StatusBadRequest, "validation", "the snapshot ID must be 8 to 64 lower-case hex digits", map[string]string{"snapshot_id": "invalid"})
		return
	}
	if badControl(want) || !path.IsAbs(want) || path.Clean(want) != want {
		badBrowsePath(w, "the path inside the snapshot must be absolute and clean, like /photos/2024")
		return
	}
	pw, err := a.repoPassword(r.Context(), t.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	defer clear(pw)
	ctx, cancel := context.WithTimeout(r.Context(), browseTimeout)
	defer cancel()
	iv, done, err := a.openRepo(ctx, t)
	if err != nil {
		internalError(w, r, err)
		return
	}
	defer done()
	if err := iv.WritePassword(pw); err != nil {
		internalError(w, r, err)
		return
	}
	cur := want
	for {
		names, found, err := restic.ListDir(ctx, iv, snapID, cur)
		switch {
		case errors.Is(err, restic.ErrNoSnapshot):
			writeError(w, http.StatusNotFound, "not_found", "no such snapshot", nil)
			return
		case err != nil:
			if !repoError(w, err) {
				internalError(w, r, err)
			}
			return
		case found:
			entries := make([]browseEntry, 0, len(names))
			for _, n := range names {
				if !badControl(n) {
					entries = append(entries, browseEntry{Name: n, Path: path.Join(cur, n)})
				}
			}
			writeListing(w, cur, cur != want, entries)
			return
		}
		cur = path.Dir(cur) // "/" is always found
	}
}
