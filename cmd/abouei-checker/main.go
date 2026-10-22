// Command abouei-checker is the entry point for the AboueiChecker CLI.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // Embed zoneinfo so the static container image can resolve local times.

	"github.com/Erfan-Abouei/Abouei_Checker/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
