package main

import (
	"context"
	"github.com/daviddwlee84/lazymlflow/internal/scoopupgrade"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/daviddwlee84/lazymlflow/internal/cli"
)

var version = "dev"

func main() {
	if code, handled := scoopupgrade.HandleHelper(scoopupgrade.Product{Binary: "lazymlflow", Module: "github.com/daviddwlee84/lazymlflow", Main: "github.com/daviddwlee84/lazymlflow/cmd/lazymlflow"}); handled {
		os.Exit(code)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(cli.Execute(ctx, os.Args[1:], cli.Options{Version: versionFromBuild()}))
}

func versionFromBuild() string {
	info, ok := debug.ReadBuildInfo()
	return resolveVersion(version, info, ok)
}
