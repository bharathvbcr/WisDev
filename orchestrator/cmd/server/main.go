package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

var version = "dev"

var (
	runFn  = Run
	exitFn = os.Exit
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runFn(ctx, version); err != nil {
		slog.Error("server exited with error", "error", err)
		exitFn(1)
	}
}
