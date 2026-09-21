package main

import (
	"context"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/daviddwlee84/lazymlflow/internal/cli"
)

var version = "dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(cli.Execute(ctx, os.Args[1:], cli.Options{Version: versionFromBuild()}))
}

func versionFromBuild() string {
	info, ok := debug.ReadBuildInfo()
	return resolveVersion(version, info, ok)
}
