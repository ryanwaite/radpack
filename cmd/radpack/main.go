package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/ryanwaite/radpack/internal/radpack"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(radpack.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
