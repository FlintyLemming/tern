// Command tern is the single-binary backup and sync node.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tern/internal/app"
	"tern/internal/config"
)

var version = "dev"

// shutdownBudget is how long the process may take to wind down after
// SIGTERM. Runs in progress get SIGTERM, then proc.KillGrace (30 s) until
// SIGKILL, then their post-task command (at most runner.ShutdownHookCap,
// 60 s), then their record written. Docker's stop grace period (compose:
// stop_grace_period 2m) has to be longer still.
const shutdownBudget = 100 * time.Second

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version":
			fmt.Println("tern", version)
			return
		case "--healthcheck":
			os.Exit(healthcheck(os.Getenv))
		}
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "tern:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a, err := app.Start(ctx, cfg, version)
	if err != nil {
		return err
	}
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()
	return a.Shutdown(shutdownCtx)
}
