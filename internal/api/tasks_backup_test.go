package api

import (
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"tern/internal/engine/restic/restictest"
	"tern/internal/model"
)

const repoPass = `p@$$'w"ö密码 x`

// nasConnID stores an SSH connection to nas2.example:22, which nothing
// ever dials: the harness preflight passes without a network.
func (h *harness) nasConnID() string {
	h.t.Helper()
	k := h.genKey("key-nas2")
	body := map[string]any{"type": "ssh", "name": "nas2", "host": "nas2.example", "port": 22, "username": "backup",
		"ssh_key_id": k["id"], "host_key": k["public_key"]}
	return wantStatus(h.t, h.do("POST", "/api/v1/connections", body), http.StatusCreated)["id"].(string)
}

func (h *harness) runtimeLeftovers() []string {
	h.t.Helper()
	entries, err := os.ReadDir(h.runtimeDir)
	if err != nil {
		h.t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestCreateBackupNeedsConfirmation(t *testing.T) {
	h := readyHarness(t)
	conn := h.sshConnID()
	for _, confirmed := range []any{nil, false} {
		body := backupBody("docs", conn)
		if confirmed == nil {
			delete(body, "recovery_confirmed")
		} else {
			body["recovery_confirmed"] = confirmed
		}
		wantError(t, h.do("POST", "/api/v1/tasks", body), http.StatusBadRequest, "recovery_not_confirmed")
	}
	if n, calls := h.count("tasks"), len(h.restic.Calls()); n != 0 || calls != 0 {
		t.Errorf("%d tasks stored, %d restic calls; want none", n, calls)
	}
}

func TestCreateBackupNewRepo(t *testing.T) {
	h := readyHarness(t)
	h.restic.Add(restictest.Step{Exit: 10}, restictest.Step{})
	task := h.newTask(backupBody("docs", h.sshConnID()))
	if task["repo_state"] != "new" {
		t.Errorf("repo_state = %v", task["repo_state"])
	}
	calls := h.restic.Calls()
	if !reflect.DeepEqual(h.restic.Subs(), []string{"cat", "init"}) {
		t.Fatalf("restic calls = %q", h.restic.Subs())
	}
	for _, c := range calls {
		if c.Password != repoPass || c.PasswordMode != 0o600 || !c.Has("-r", "sftp:tern-target:/srv/restic/docs") {
			t.Errorf("call %q: password %q mode %o", c.Args, c.Password, c.PasswordMode)
		}
		for _, a := range c.Args {
			if strings.Contains(a, repoPass) {
				t.Errorf("password in argv: %q", c.Args)
			}
		}
	}
	if left := h.runtimeLeftovers(); len(left) != 0 {
		t.Errorf("runtime directory still holds %q", left)
	}
}

func TestCreateBackupExistingRepo(t *testing.T) {
	h := readyHarness(t)
	task := h.newTask(backupBody("docs", h.sshConnID()))
	if task["repo_state"] != "existing" || !reflect.DeepEqual(h.restic.Subs(), []string{"cat"}) {
		t.Errorf("repo_state = %v, restic calls %q", task["repo_state"], h.restic.Subs())
	}
	if _, has := task["repo_password"]; has {
		t.Error("the response has the repository password")
	}
}

func TestCreateBackupWrongPassword(t *testing.T) {
	h := readyHarness(t)
	h.restic.Add(restictest.Step{Exit: 12})
	wantError(t, h.do("POST", "/api/v1/tasks", backupBody("docs", h.sshConnID())), http.StatusBadRequest, "wrong_repo_password")
	if n := h.count("tasks"); n != 0 {
		t.Errorf("%d tasks stored", n)
	}
	if left := h.runtimeLeftovers(); len(left) != 0 {
		t.Errorf("runtime directory still holds %q", left)
	}
}

func TestCreateBackupUnreachable(t *testing.T) {
	h := readyHarness(t)
	conn := h.sshConnID()
	h.check = func(string) error {
		return &model.KindError{Kind: model.KindNetwork, Msg: "cannot reach nas2.example:22"}
	}
	wantError(t, h.do("POST", "/api/v1/tasks", backupBody("docs", conn)), http.StatusBadGateway, "unreachable")
	if n, calls := h.count("tasks"), len(h.restic.Calls()); n != 0 || calls != 0 {
		t.Errorf("%d tasks stored, %d restic calls; want none", n, calls)
	}
}

func TestCreateBackupRepoUnreachable(t *testing.T) {
	h := readyHarness(t)
	h.restic.Add(restictest.Step{Exit: 1, Stderr: "subprocess ssh: Connection reset by peer\n"})
	e := wantError(t, h.do("POST", "/api/v1/tasks", backupBody("docs", h.sshConnID())), http.StatusBadGateway, "unreachable")
	if msg := e["message"].(string); !strings.Contains(msg, "Connection reset by peer") {
		t.Errorf("message = %q", msg)
	}
	if n := h.count("tasks"); n != 0 {
		t.Errorf("%d tasks stored", n)
	}
}

func TestCreateBackupBadPassword(t *testing.T) {
	h := readyHarness(t)
	conn := h.sshConnID()
	for _, pw := range []any{" x", "x ", "a\nb", "", nil} {
		body := backupBody("docs", conn)
		if pw == nil {
			delete(body, "repo_password")
		} else {
			body["repo_password"] = pw
		}
		e := wantError(t, h.do("POST", "/api/v1/tasks", body), http.StatusBadRequest, "validation")
		wantField(t, e, "repo_password")
	}
	if n, calls := h.count("tasks"), len(h.restic.Calls()); n != 0 || calls != 0 {
		t.Errorf("%d tasks stored, %d restic calls; want none", n, calls)
	}
}

func TestCreateBackupValidatesBeforeAttach(t *testing.T) {
	h := readyHarness(t)
	body := backupBody("docs", h.sshConnID())
	body["backup"].(map[string]any)["sources"] = []string{"relative/path"}
	wantField(t, wantError(t, h.do("POST", "/api/v1/tasks", body), http.StatusBadRequest, "validation"), "backup.sources[0]")
	if calls := len(h.restic.Calls()); calls != 0 {
		t.Errorf("%d restic calls for an invalid task", calls)
	}
}

func TestCreateBackupDuplicateName(t *testing.T) {
	h := readyHarness(t)
	conn := h.sshConnID()
	h.newTask(backupBody("docs", conn))
	before := len(h.restic.Calls())
	wantError(t, h.do("POST", "/api/v1/tasks", backupBody("docs", conn)), http.StatusConflict, "conflict")
	if len(h.restic.Calls()) != before {
		t.Error("a repository was touched for a task whose name is taken")
	}
}

func TestSyncTaskRejectsRepoPassword(t *testing.T) {
	h := readyHarness(t)
	body := syncBody("photos", h.sshConnID())
	body["repo_password"] = "whatever"
	wantField(t, wantError(t, h.do("POST", "/api/v1/tasks", body), http.StatusBadRequest, "validation"), "repo_password")
}

func TestHostFromNodeName(t *testing.T) {
	h := readyHarness(t)
	body := backupBody("docs", h.sshConnID())
	body["backup"].(map[string]any)["host"] = "evil"
	task := h.newTask(body)
	if host := task["backup"].(map[string]any)["host"]; host != "nas-1" {
		t.Fatalf("backup.host = %v, want the node name", host)
	}
	wantStatus(t, h.do("PUT", "/api/v1/settings", settingsBody(func(b map[string]any) { b["node_name"] = "nas-2" })), http.StatusOK)
	got := wantStatus(t, h.do("GET", taskPath(task), nil), http.StatusOK)
	if host := got["backup"].(map[string]any)["host"]; host != "nas-1" {
		t.Errorf("backup.host = %v after renaming the node", host)
	}
}

func TestRepoPasswordSealed(t *testing.T) {
	h := readyHarness(t)
	task := h.newTask(backupBody("docs", h.sshConnID()))
	sealed := h.column("SELECT repo_password FROM tasks WHERE id = ?", task["id"])
	if !strings.HasPrefix(sealed, "v1.") || strings.Contains(sealed, repoPass) {
		t.Errorf("repo_password column = %q", sealed)
	}
	if spec := h.column("SELECT spec FROM tasks WHERE id = ?", task["id"]); strings.Contains(spec, "repo_password") {
		t.Errorf("spec column = %s", spec)
	}
	for _, path := range []string{taskPath(task), "/api/v1/tasks", "/api/v1/overview"} {
		w := h.do("GET", path, nil)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "ö密码") || strings.Contains(w.Body.String(), "repo_password") {
			t.Errorf("GET %s = %d %s", path, w.Code, w.Body)
		}
	}
}

func TestGenerateRepoPasswordEndpoint(t *testing.T) {
	h := readyHarness(t)
	re := regexp.MustCompile(`^[A-Za-z0-9]{32}$`)
	a := wantStatus(t, h.do("POST", "/api/v1/tasks/repo-password", nil), http.StatusOK)["password"].(string)
	b := wantStatus(t, h.do("POST", "/api/v1/tasks/repo-password", nil), http.StatusOK)["password"].(string)
	if !re.MatchString(a) || !re.MatchString(b) || a == b {
		t.Errorf("generated %q and %q", a, b)
	}
	h.cookie = ""
	wantError(t, h.do("POST", "/api/v1/tasks/repo-password", nil), http.StatusUnauthorized, "unauthorized")
}

func TestRecoveryInfo(t *testing.T) {
	h := readyHarness(t)
	body := backupBody("docs", h.nasConnID())
	body["backup"].(map[string]any)["repo_path"] = "/srv/repo"
	task := h.newTask(body)
	got := wantStatus(t, h.do("POST", taskPath(task)+"/recovery-info", map[string]any{"password": adminPass}), http.StatusOK)
	if got["repository"] != "sftp:backup@nas2.example:/srv/repo" || got["password"] != repoPass {
		t.Errorf("recovery info = %v", got)
	}
	cmds, _ := got["commands"].([]any)
	if len(cmds) != 4 || cmds[0] != "export RESTIC_REPOSITORY='sftp:backup@nas2.example:/srv/repo'" ||
		cmds[1] != `export RESTIC_PASSWORD='p@$$'\''w"ö密码 x'` || cmds[2] != "restic snapshots" {
		t.Errorf("commands = %q", cmds)
	}
	if len(got) != 3 {
		t.Errorf("unexpected fields: %v", got)
	}
}

func TestRecoveryInfoWrongAdminPassword(t *testing.T) {
	h := readyHarness(t)
	task := h.newTask(backupBody("docs", h.sshConnID()))
	bad := map[string]any{"password": "not the admin password"}
	for i := range 5 {
		w := h.do("POST", taskPath(task)+"/recovery-info", bad)
		wantError(t, w, http.StatusForbidden, "forbidden")
		if strings.Contains(w.Body.String(), "ö密码") {
			t.Fatalf("attempt %d leaked the password", i+1)
		}
	}
	wantError(t, h.do("POST", taskPath(task)+"/recovery-info", bad), http.StatusTooManyRequests, "rate_limited")
	wantError(t, h.do("POST", taskPath(task)+"/recovery-info", map[string]any{"password": adminPass}), http.StatusTooManyRequests, "rate_limited")
}

func TestRecoveryInfoNotABackup(t *testing.T) {
	h := readyHarness(t)
	task := h.newSyncTask("photos")
	wantError(t, h.do("POST", taskPath(task)+"/recovery-info", map[string]any{"password": adminPass}), http.StatusBadRequest, "validation")
	wantError(t, h.do("POST", "/api/v1/tasks/nope/recovery-info", map[string]any{"password": adminPass}), http.StatusNotFound, "not_found")
}

func TestUpdateRejectsRepoPassword(t *testing.T) {
	h := readyHarness(t)
	task := h.newTask(backupBody("docs", h.sshConnID()))
	delete(task, "repo_state")
	if w := h.update(task, func(b map[string]any) { b["repo_password"] = "another one" }).w; w.Code != http.StatusBadRequest {
		t.Fatalf("PUT with repo_password = %d %s", w.Code, w.Body)
	}
	if w := h.update(task, func(b map[string]any) { b["name"] = "renamed" }).w; w.Code != http.StatusOK {
		t.Fatalf("plain PUT = %d %s", w.Code, w.Body)
	}
	got := wantStatus(t, h.do("POST", taskPath(task)+"/recovery-info", map[string]any{"password": adminPass}), http.StatusOK)
	if got["password"] != repoPass {
		t.Errorf("password after an update = %v", got["password"])
	}
}
