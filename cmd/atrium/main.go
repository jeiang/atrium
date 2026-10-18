package main

import (
	"os"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/logx"
)

func main() {
	os.Exit(newApp().Run(os.Args[1:], os.Stdout, os.Stderr))
}

func newApp() *cli.App {
	return cli.New("atrium", logx.Setup)
}
