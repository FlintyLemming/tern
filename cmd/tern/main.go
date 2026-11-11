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

	// Runs in progress get SIGTERM, then proc.KillGrace (30s) until SIGKILL,
	// then their record written: leave room for that. The container's stop
	// grace period has to be at least as long.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return a.Shutdown(shutdownCtx)
}
