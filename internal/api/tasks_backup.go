package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"tern/internal/engine/restic"
	"tern/internal/model"
	"tern/internal/rundir"
)

// repoTimeout bounds what a request may spend talking to a repository.
const repoTimeout = 2 * time.Minute

func (a *API) registerBackup() {
	g := []Guard{NeedSession, NeedUnlocked}
	a.Handle("POST /api/v1/tasks/repo-password", a.handleGenerateRepoPassword, g...)
	a.Handle("POST /api/v1/tasks/{id}/recovery-info", a.handleRecoveryInfo, g...)
}

// createTaskRequest is a task plus what only exists while a backup task
// is being created.
type createTaskRequest struct {
	model.Task
	RepoPassword      *string `json:"repo_password"`
	RecoveryConfirmed *bool   `json:"recovery_confirmed"`
}

type createdBackupJSON struct {
	model.Task
	RepoState restic.RepoState `json:"repo_state"`
}

func (a *API) handleGenerateRepoPassword(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"password": restic.GenerateRepoPassword()})
}

// openRepo prepares restic calls against the repository of t outside a
// run: a private tmpfs directory with the SSH material. The caller puts
// the password there (restic.Attach, Invocation.WritePassword) and calls
// done when finished.
func (a *API) openRepo(ctx context.Context, t model.Task) (iv restic.Invocation, done func(), err error) {
	dir, err := rundir.New(a.d.Cfg.RuntimeDir, "api-"+model.NewID())
	if err != nil {
		return iv, nil, err
	}
	acc, release, err := a.d.Access.Open(ctx, t.Backup.ConnectionID, dir, nil)
	if err != nil {
		dir.Close()
		return iv, nil, err
	}
	iv = restic.Invocation{Bin: a.d.ResticBin, SSHConfig: acc.SSHConfig, Dir: dir, DataDir: a.d.Cfg.DataDir,
		RepoPath: t.Backup.RepoPath, Connections: t.Backup.Connections}
	return iv, func() { release(); dir.Close() }, nil
}

// repoError answers a failed attempt to reach a repository. On false the
// error was not one of those and nothing has been written.
func repoError(w http.ResponseWriter, err error) bool {
	var ke *model.KindError
	if !errors.As(err, &ke) {
		return false
	}
	if ke.Kind == model.KindAuth {
		writeError(w, http.StatusBadRequest, "wrong_repo_password", "the repository exists and this is not its password", nil)
	} else {
		writeError(w, http.StatusBadGateway, "unreachable", ke.Error(), nil)
	}
	return true
}

// createBackupTask stores a backup task only after everything that could
// make it useless has been ruled out: the recovery information was taken
// down, the remote end answers, and the repository is there (or was just
// created) and opens with the password. If any step fails no task is left.
func (a *API) createBackupTask(w http.ResponseWriter, r *http.Request, t model.Task, req createTaskRequest) {
	if req.RecoveryConfirmed == nil || !*req.RecoveryConfirmed {
		// If this node dies the password is gone with it, and the backups
		// with the password.
		writeError(w, http.StatusBadRequest, "recovery_not_confirmed",
			"save the recovery information (repository address and password) and confirm it before creating a backup task", nil)
		return
	}
	switch {
	case req.RepoPassword == nil || *req.RepoPassword == "":
		writeError(w, http.StatusBadRequest, "validation", "invalid task", map[string]string{"repo_password": "required"})
		return
	case restic.ValidateRepoPassword(*req.RepoPassword) != nil:
		writeError(w, http.StatusBadRequest, "validation", "invalid task", map[string]string{"repo_password": "invalid"})
		return
	}
	password := []byte(*req.RepoPassword)

	b := t.Backup
	// Defaults that only apply to a new task: afterwards 0 means "never".
	if b.PruneEveryDays == 0 {
		b.PruneEveryDays = 7
	}
	if b.CheckEveryDays == 0 {
		b.CheckEveryDays = 30
	}
	// Snapshots are filed under the node name of the day the task was
	// created, whatever the client sent; renaming the node later must not
	// split them.
	name, _, err := a.d.Store.GetMeta(r.Context(), metaNodeName)
	if err != nil {
		internalError(w, r, err)
		return
	}
	b.Host = name

	fields := t.Validate()
	if err := a.checkTaskRefs(r, t, fields); err != nil {
		internalError(w, r, err)
		return
	}
	if len(fields) > 0 {
		writeError(w, http.StatusBadRequest, "validation", "invalid task", fields)
		return
	}
	// Before a repository is touched for it.
	tasks, err := a.d.Store.ListTasks(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	for _, other := range tasks {
		if other.Name == t.Name {
			writeError(w, http.StatusConflict, "conflict", "another task already has this name", nil)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), repoTimeout)
	defer cancel()
	if err := a.d.Access.Check(ctx, b.ConnectionID); err != nil {
		var ke *model.KindError
		if errors.As(err, &ke) {
			writeError(w, http.StatusBadGateway, "unreachable", ke.Error(), nil)
			return
		}
		internalError(w, r, err)
		return
	}
	iv, done, err := a.openRepo(ctx, t)
	if err != nil {
		internalError(w, r, err)
		return
	}
	state, err := restic.Attach(ctx, iv, password)
	done()
	if err != nil {
		if !repoError(w, err) {
			internalError(w, r, err)
		}
		return
	}

	sealed, err := a.d.Vault.Seal(restic.PasswordAAD(t.ID), password)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if err := a.d.Store.CreateTask(r.Context(), t, sealed); err != nil {
		storeError(w, r, err, "task")
		return
	}
	a.taskChanged(t.ID)
	stored, err := a.d.Store.GetTask(r.Context(), t.ID)
	if err != nil {
		storeError(w, r, err, "task")
		return
	}
	writeJSON(w, http.StatusCreated, createdBackupJSON{Task: stored, RepoState: state})
}

// repoPassword unseals the repository password of a backup task.
func (a *API) repoPassword(ctx context.Context, taskID string) ([]byte, error) {
	sealed, err := a.d.Store.TaskRepoPassword(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if sealed == "" {
		return nil, errors.New("api: backup task " + taskID + " has no repository password")
	}
	return a.d.Vault.Open(restic.PasswordAAD(taskID), sealed)
}

// backupTask loads the task named in the path and makes sure it is a
// backup task. On false the response has been written.
func (a *API) backupTask(w http.ResponseWriter, r *http.Request) (model.Task, bool) {
	t, err := a.d.Store.GetTask(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, r, err, "task")
		return t, false
	}
	if t.Type != model.TaskBackup || t.Backup == nil {
		writeError(w, http.StatusBadRequest, "validation", "the task is not a backup task", map[string]string{"type": "invalid"})
		return t, false
	}
	return t, true
}

// handleRecoveryInfo shows the recovery information again. It contains
// the repository password, so the admin password is asked for once more.
func (a *API) handleRecoveryInfo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	t, ok := a.backupTask(w, r)
	if !ok {
		return
	}
	if !a.confirmPassword(w, r, req.Password) {
		return
	}
	conn, err := a.d.Store.GetConnection(r.Context(), t.Backup.ConnectionID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	pw, err := a.repoPassword(r.Context(), t.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	info := restic.RecoveryInfo(conn, t.Backup.RepoPath, string(pw))
	writeJSON(w, http.StatusOK, map[string]any{"repository": info.Repository, "password": info.Password, "commands": info.Commands})
}
