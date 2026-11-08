// Command toolsdocs writes or checks docs/tools.md against the production
// tool registry. It needs no database or module settings, so CI can run it
// without bootstrapping the app.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/jeiang/atrium/internal/assistant/toolset"
)

func main() {
	check := flag.Bool("check", false, "fail instead of writing when the catalog is stale")
	path := flag.String("path", "docs/tools.md", "catalog path")
	flag.Parse()
	reg, err := toolset.Registry()
	if err != nil {
		fail(err)
	}
	tools, err := reg.All()
	if err != nil {
		fail(err)
	}
	want := toolset.Render(tools)
	if *check {
		have, err := os.ReadFile(*path)
		if err != nil {
			fail(err)
		}
		if string(have) != want {
			fail(fmt.Errorf("%s is stale; run `just tools-docs`", *path))
		}
		return
	}
	if err := os.WriteFile(*path, []byte(want), 0o644); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
