package main

import (
	"io"
	"log/slog"
	"os"

	"github.com/jeiang/atrium/internal/cli"
)

func main() {
	os.Exit(newApp().Run(os.Args[1:], os.Stdout, os.Stderr))
}

func newApp() *cli.App {
	return cli.New("atrium", setupLogging)
}

func setupLogging(stderr io.Writer) {
	slog.SetDefault(slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
}
