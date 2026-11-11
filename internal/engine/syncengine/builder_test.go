package syncengine

import (
	"context"
	"strings"
	"testing"
	"time"

	"tern/internal/access"
	"tern/internal/engine"
	"tern/internal/model"
	"tern/internal/redact"
	"tern/internal/rundir"
	"tern/internal/store"
)

const davPass = "hunter2-dav-secret"

type nopAccess struct{}

func (nopAccess) Open(ctx context.Context, connID string, dir *rundir.Dir, red *redact.Redactor) (access.Access, func(), error) {
	return access.Access{}, func() {}, nil
}

func (nopAccess) Check(ctx context.Context, connID string) error { return nil }

// newStore holds an SSH connection "ssh" to backup@nas2.example:22, one
// "ssh6" to an IPv6 address and a WebDAV connection "dav".
func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx, at := context.Background(), time.Unix(1_700_000_000, 0).UTC()
	key := model.SSHKey{ID: "k1", Version: 1, Name: "key", PublicKey: "ssh-ed25519 AAAA", Fingerprint: "SHA256:x", CreatedAt: at, UpdatedAt: at}
	if err := st.CreateKey(ctx, key, "v1.priv", ""); err != nil {
		t.Fatal(err)
	}
	for _, c := range []model.Connection{
		{ID: "ssh", Version: 1, Type: model.ConnSSH, Name: "nas2", Host: "nas2.example", Port: 22, Username: "backup", SSHKeyID: "k1", HostKey: "ssh-ed25519 AAAA", CreatedAt: at, UpdatedAt: at},
		{ID: "ssh6", Version: 1, Type: model.ConnSSH, Name: "nas6", Host: "fd7a:115c::1", Port: 22, Username: "backup", SSHKeyID: "k1", HostKey: "ssh-ed25519 AAAA", CreatedAt: at, UpdatedAt: at},
		{ID: "dav", Version: 1, Type: model.ConnWebDAV, Name: "dav", URL: "https://dav.example/remote.php/dav", Username: "anna", CreatedAt: at, UpdatedAt: at},
	} {
		if err := st.CreateConnection(ctx, c, "v1.sealed-"+davPass); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func task(protocol, connID, direction, mode string, pairs ...model.FolderPair) model.Task {
	t := model.Task{ID: "t1", Type: model.TaskSync, Name: "photos", Sync: &model.SyncSpec{
		Protocol: protocol, ConnectionID: connID, Direction: direction, DeleteMode: mode, Pairs: pairs}}
	t.ApplyDefaults()
	return t
}

var pairA = model.FolderPair{Local: "/sources/a", Remote: "/srv/a"}

func describe(t *testing.T, st *store.Store, tk model.Task) []string {
	t.Helper()
	eng, err := NewBuilder(Deps{Access: nopAccess{}, Store: st})(tk, engine.Request{Kind: model.RunSync})
	if err != nil {
		t.Fatal(err)
	}
	return eng.Describe()
}

func TestBuilderPicksPlanner(t *testing.T) {
	st := newStore(t)
	for _, c := range []struct{ protocol, conn, prefix string }{
		{"sftp", "ssh", "rclone "}, {"webdav", "dav", "rclone "}, {"rsync", "ssh", "rsync "},
	} {
		lines := describe(t, st, task(c.protocol, c.conn, "push", "keep", pairA, model.FolderPair{Local: "/sources/b", Remote: "/srv/b"}))
		if len(lines) != 2 {
			t.Errorf("%s: %d lines", c.protocol, len(lines))
		}
		for _, l := range lines {
			if !strings.HasPrefix(l, c.prefix) {
				t.Errorf("%s: %q", c.protocol, l)
			}
		}
	}
}

func TestBuilderRejectsOtherKinds(t *testing.T) {
	b := NewBuilder(Deps{Access: nopAccess{}, Store: newStore(t)})
	for _, kind := range []model.RunKind{model.RunPrune, model.RunCheck, model.RunBackup, model.RunRestore} {
		if _, err := b(task("sftp", "ssh", "push", "keep", pairA), engine.Request{Kind: kind}); model.KindOf(err) != model.KindConfig {
			t.Errorf("%s: err = %v, want config", kind, err)
		}
	}
	if _, err := b(task("sftp", "ssh", "push", "keep", pairA), engine.Request{Kind: model.RunDryRun}); err != nil {
		t.Errorf("dry_run: %v", err)
	}
}

func TestBuilderMissingConnection(t *testing.T) {
	b := NewBuilder(Deps{Access: nopAccess{}, Store: newStore(t)})
	if _, err := b(task("sftp", "gone", "push", "keep", pairA), engine.Request{Kind: model.RunSync}); model.KindOf(err) != model.KindConfig {
		t.Errorf("err = %v, want config", err)
	}
}

func TestDescribeRsyncPushMirror(t *testing.T) {
	got := describe(t, newStore(t), task("rsync", "ssh", "push", "mirror", pairA))
	want := `rsync -aH --partial-dir=.tern-partial --timeout=60 --delete -e "ssh -p 22" /sources/a/ backup@nas2.example:/srv/a/`
	if len(got) != 1 || got[0] != want {
		t.Errorf("Describe = %q\nwant %q", got, want)
	}
}

func TestDescribeRsyncOptions(t *testing.T) {
	tk := task("rsync", "ssh", "pull", "keep", model.FolderPair{Local: "/targets/my photos", Remote: "/srv/a"})
	tk.Sync.LimitKiB, tk.Sync.Compare = 2048, "checksum"
	got := describe(t, newStore(t), tk)
	want := `rsync -aH --partial-dir=.tern-partial --timeout=60 --bwlimit=2048 --checksum -e "ssh -p 22" backup@nas2.example:/srv/a/ '/targets/my photos/'`
	if len(got) != 1 || got[0] != want {
		t.Errorf("Describe = %q\nwant %q", got, want)
	}
}

func TestDescribeRsyncIPv6(t *testing.T) {
	got := describe(t, newStore(t), task("rsync", "ssh6", "push", "keep", pairA))
	if len(got) != 1 || !strings.HasSuffix(got[0], " backup@[fd7a:115c::1]:/srv/a/") {
		t.Errorf("Describe = %q", got)
	}
}

func TestDescribeSFTPPull(t *testing.T) {
	got := describe(t, newStore(t), task("sftp", "ssh", "pull", "keep", model.FolderPair{Local: "/targets/a", Remote: "/srv/a"}))
	if want := "rclone copy :sftp,host=nas2.example,user=backup,port=22:/srv/a /targets/a --transfers 4"; len(got) != 1 || got[0] != want {
		t.Errorf("Describe = %q\nwant %q", got, want)
	}
}

func TestDescribeSFTPOptions(t *testing.T) {
	tk := task("sftp", "ssh6", "push", "mirror", pairA)
	tk.Sync.LimitKiB, tk.Sync.Compare = 512, "checksum"
	got := describe(t, newStore(t), tk)
	want := `rclone sync /sources/a ':sftp,host="fd7a:115c::1",user=backup,port=22:/srv/a' --transfers 4 --bwlimit 512Ki --checksum`
	if len(got) != 1 || got[0] != want {
		t.Errorf("Describe = %q\nwant %q", got, want)
	}
}

func TestDescribeHasNoSecrets(t *testing.T) {
	got := describe(t, newStore(t), task("webdav", "dav", "push", "keep", pairA))
	want := `rclone copy /sources/a ':webdav,url="https://dav.example/remote.php/dav",user=anna:/srv/a' --webdav-pass "$(rclone obscure '<WebDAV 密码>')" --transfers 4`
	if len(got) != 1 || got[0] != want {
		t.Errorf("Describe = %q\nwant %q", got, want)
	}
	if strings.Contains(strings.Join(got, "\n"), davPass) {
		t.Error("the password is in the command line")
	}
}

// Without its exclude rules the command would not be equivalent.
func TestDescribeExcludes(t *testing.T) {
	st := newStore(t)
	for protocol, want := range map[string]string{
		"rsync": ` '--exclude=*.tmp' --exclude=/cache/ -e "ssh -p 22" `,
		"sftp":  ` --transfers 4 --filter '- *.tmp' --filter '- /cache/'`,
	} {
		tk := task(protocol, "ssh", "push", "keep", pairA)
		tk.Sync.Excludes = []string{"*.tmp", "/cache/"}
		if got := describe(t, st, tk); len(got) != 1 || !strings.Contains(got[0], want) {
			t.Errorf("%s: Describe = %q, want it to contain %q", protocol, got, want)
		}
	}
}
