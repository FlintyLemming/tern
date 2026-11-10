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

	"tern/internal/api"
	"tern/internal/auth"
	"tern/internal/config"
	"tern/internal/rundir"
	"tern/internal/store"
	"tern/internal/vault"
)

// App is a running tern node.
type App struct {
	st  *store.Store
	api *api.API
	srv *http.Server
	ln  net.Listener
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

	h := api.New(api.Deps{
		Cfg:      cfg,
		Store:    st,
		Vault:    v,
		Sessions: auth.NewSessions(st.DB(), time.Now),
		Limiter:  auth.NewLimiter(),
		Version:  version,
		Now:      time.Now,
	})

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	a := &App{
		st:  st,
		api: h,
		ln:  ln,
		srv: &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second},
	}
	go func() {
		if err := a.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server stopped", "err", err)
		}
	}()
	slog.Info("tern started", "version", version, "addr", a.Addr(), "tls", tlsCfg != nil, "vault_state", string(state))
	return a, nil
}

// Addr is the address actually being listened on.
func (a *App) Addr() string { return a.ln.Addr().String() }

// API is where later components register their routes.
func (a *App) API() *api.API { return a.api }

// Shutdown stops accepting requests, waits for in-flight ones until ctx
// expires, then closes the database and releases the data directory.
func (a *App) Shutdown(ctx context.Context) error {
	err := a.srv.Shutdown(ctx)
	if err != nil {
		a.srv.Close()
	}
	return errors.Join(err, a.st.Close())
}
