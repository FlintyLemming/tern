// Package app wires the process together and shuts it down cleanly.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"tern/internal/access"
	"tern/internal/api"
	"tern/internal/auth"
	"tern/internal/clock"
	"tern/internal/config"
	"tern/internal/engine"
	"tern/internal/engine/restic"
	"tern/internal/engine/syncengine"
	"tern/internal/events"
	"tern/internal/hooks"
	"tern/internal/model"
	"tern/internal/mounts"
	"tern/internal/rundir"
	"tern/internal/runner"
	"tern/internal/scheduler"
	"tern/internal/store"
	"tern/internal/sysinfo"
	"tern/internal/vault"
	"tern/web"
)

// App is a running tern node.
type App struct {
	st    *store.Store
	api   *api.API
	sched *scheduler.Scheduler
	srv   *http.Server
	ln    net.Listener
}

// Start opens the data directory, loads the vault and begins serving.
// When it returns an error nothing is left open.
func Start(ctx context.Context, cfg config.Config, version string) (*App, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return nil, err
	}
	a, err := startWithStore(ctx, cfg, version, st)
	if err != nil {
		st.Close()
		return nil, err
	}
	return a, nil
}

// prepareRuntimeDir makes cfg.RuntimeDir a private, empty tmpfs directory.
// It runs only once the data directory lock is held, so the leftovers it
// removes cannot belong to a live process.
func prepareRuntimeDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create runtime directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("runtime directory: %w", err)
	}
	if err := rundir.VerifyTmpfs(dir); err != nil {
		return err
	}
	if err := rundir.Sweep(dir); err != nil {
		return fmt.Errorf("clear runtime directory: %w", err)
	}
	return nil
}

// newRegistry registers the engines: restic for backup tasks, rclone and
// rsync (by protocol) for sync tasks.
func newRegistry(cfg config.Config, st *store.Store, v *vault.Vault) *engine.Registry {
	opener := access.Opener{Store: st, Vault: v}
	registry := engine.NewRegistry()
	registry.Register(model.TaskBackup, restic.NewBuilder(restic.Deps{
		Access:  opener,
		Store:   st,
		Vault:   v,
		DataDir: cfg.DataDir,

		SecretsDir: cfg.SecretsDir,
	}))
	registry.Register(model.TaskSync, syncengine.NewBuilder(syncengine.Deps{Access: opener, Store: st}))
	return registry
}

func startWithStore(ctx context.Context, cfg config.Config, version string, st *store.Store) (*App, error) {
	if err := prepareRuntimeDir(cfg.RuntimeDir); err != nil {
		return nil, err
	}
	v := vault.New(st, cfg.SecretsDir)
	state, err := v.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("load vault: %w", err)
	}

	var tlsCfg *tls.Config
	if cfg.TLSCert != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return nil, fmt.Errorf("load TLS certificate: %w", err)
		}
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}

	// The execution core.
	hub := events.NewHub()
	registry := newRegistry(cfg, st, v)
	run := &runner.Runner{
		Store:      st,
		Registry:   registry,
		Hooks:      hooks.Runner{Access: access.Opener{Store: st, Vault: v}, RuntimeDir: cfg.RuntimeDir},
		Events:     hub,
		Clock:      clock.Real,
		DataDir:    cfg.DataDir,
		RuntimeDir: cfg.RuntimeDir,
	}
	sched := scheduler.New(scheduler.Deps{Store: st, Runner: run, Clock: clock.Real, Events: hub})
	// No run can get at its credentials before the vault is open; the API
	// resumes scheduling once setup or unlock has happened.
	sched.SetPaused(state != vault.StateUnlocked)

	// Without these volumes the database or the key file would land in
	// the container's own layer; setup refuses then, and the log says why.
	mnt := sysinfo.CheckMounts(cfg, mounts.IsMounted)
	if !mnt.Data {
		slog.Warn("the data directory is not a mounted volume: setup is refused; add a volume for it, e.g. ./data:/data", "dir", cfg.DataDir)
	}
	if !mnt.Secrets {
		slog.Warn("the secrets directory is not a mounted volume: key file mode is unavailable; add a volume for it, e.g. ./secrets:/secrets", "dir", cfg.SecretsDir)
	}

	h := api.New(api.Deps{
		Cfg:       cfg,
		Mounts:    mnt,
		Store:     st,
		Vault:     v,
		Sessions:  auth.NewSessions(st.DB(), time.Now),
		Limiter:   auth.NewLimiter(),
		Version:   version,
		Now:       time.Now,
		Clock:     clock.Real,
		Scheduler: sched,
		Runner:    run,
		Registry:  registry,
		Events:    hub,
	})
	h.ServeStatic(web.Dist())

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	// Last thing that can fail, and the first that has side effects on run
	// records: leftovers of the previous process become "interrupted".
	if err := sched.Start(ctx); err != nil {
		ln.Close()
		return nil, fmt.Errorf("start scheduler: %w", err)
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	a := &App{
		st:    st,
		api:   h,
		sched: sched,
		ln:    ln,
		srv:   &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second},
	}
	go func() {
		if err := a.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server stopped", "err", err)
		}
	}()
	slog.Info("tern started", "version", version, "addr", a.Addr(), "tls", tlsCfg != nil, "vault_state", string(state),
		"platform", cfg.Platform, "base_path", cfg.BasePath)
	return a, nil
}

// Addr is the address actually being listened on.
func (a *App) Addr() string { return a.ln.Addr().String() }

// API is where later components register their routes.
func (a *App) API() *api.API { return a.api }

// Shutdown stops accepting requests and waits for in-flight ones, then
// interrupts every run and waits for it to wind down (children terminated,
// post-task commands run, records written), all until ctx expires. Last it
// closes the database and releases the data directory.
func (a *App) Shutdown(ctx context.Context) error {
	a.api.Close() // event streams would otherwise never go idle
	err := a.srv.Shutdown(ctx)
	if err != nil {
		a.srv.Close()
	}
	a.sched.Shutdown(ctx)
	return errors.Join(err, a.st.Close())
}
