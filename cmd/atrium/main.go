package main

import (
	"os"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/logx"
)

var version = "dev"

func main() {
	os.Exit(newApp().Run(os.Args[1:], os.Stdout, os.Stderr))
}

func newApp() *cli.App {
	app := cli.New("atrium", logx.Setup)
	app.Version = version
	return app
}
