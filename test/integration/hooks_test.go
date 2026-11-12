//go:build integration

package integration

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"tern/internal/access"
	"tern/internal/hooks"
	"tern/internal/model"
	"tern/internal/sshx"
	"tern/internal/store"
	"tern/internal/vault"
	kit "tern/test/testkit"
)

// storedSSH puts a key the remote end accepts and a connection to it into a
// fresh store, the way the API would, and returns an Opener over it.
func storedSSH(t *testing.T) (access.Opener, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	v := vault.New(st, t.TempDir())
	if _, err := v.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := v.Init(ctx, vault.ModeKeyfile, ""); err != nil {
		t.Fatal(err)
	}
	privPEM, p, err := sshx.Generate()
	if err != nil {
		t.Fatal(err)
	}
	kit.AppendAuthorized(t, p)
	tg := kit.Target(t)
	now := time.Now().UTC().Truncate(time.Second)
	keyID, connID := model.NewID(), model.NewID()
	sealed, err := v.Seal("ssh_keys/"+keyID+"/private", privPEM)
	if err != nil {
		t.Fatal(err)
	}
	k := model.SSHKey{ID: keyID, Version: 1, Name: "k", PublicKey: p.Authorized, Fingerprint: p.Fingerprint, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateKey(ctx, k, sealed, ""); err != nil {
		t.Fatal(err)
	}
	c := model.Connection{ID: connID, Version: 1, Type: model.ConnSSH, Name: "c", Host: tg.Host, Port: tg.Port,
		Username: tg.User, SSHKeyID: keyID, HostKey: tg.HostKey, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateConnection(ctx, c, ""); err != nil {
		t.Fatal(err)
	}
	return access.Opener{Store: st, Vault: v}, connID
}

func TestRemoteHookRealSSH(t *testing.T) {
	dir := kit.RemotePath(t)
	opener, connID := storedSSH(t)
	runtime, err := os.MkdirTemp("/dev/shm", "th")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(runtime) })

	const name = "it's 照片"
	r := hooks.Runner{Access: opener, RuntimeDir: runtime}
	h := model.Hook{Script: `printf %s "$TERN_TASK_NAME" > ` + dir + `/hook.txt`, Where: model.HookSSH, ConnectionID: connID, TimeoutSec: 60}
	v := hooks.Vars{TaskID: "t1", TaskName: name, TaskType: "sync", Trigger: "manual"}
	var log bytes.Buffer
	if err := r.Run(context.Background(), h, v, &log); err != nil {
		t.Fatalf("hook: %v\n%s", err, log.String())
	}
	got, stderr, code := kit.RunSSH(t, kit.OpenSSH(t, kit.Authorize(t), kit.Target(t)), "cat", dir+"/hook.txt")
	if code != 0 || got != name {
		t.Errorf("remote file = %q (exit %d, %s), want %q", got, code, stderr, name)
	}

	// A failing remote script reports its exit code.
	h.Script = "exit 4"
	if err := r.Run(context.Background(), h, v, &log); err == nil || !bytes.Contains([]byte(err.Error()), []byte("exit 4")) {
		t.Errorf("err = %v, want exit 4", err)
	}
}
