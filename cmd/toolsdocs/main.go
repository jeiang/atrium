package main

import (
	"fmt"
	"os"

	"github.com/jeiang/atrium/internal/assistant/toolset"
)

func main() {
	const path = "docs/tools.md"
	check := len(os.Args) == 2 && os.Args[1] == "check"
	if len(os.Args) > 1 && !check {
		fail(fmt.Errorf("usage: toolsdocs [check]"))
	}
	reg, err := toolset.Registry()
	if err != nil {
		fail(err)
	}
	tools, err := reg.All()
	if err != nil {
		fail(err)
	}
	want := toolset.Render(tools)
	if check {
		have, err := os.ReadFile(path)
		if err != nil {
			fail(err)
		}
		if string(have) != want {
			fail(fmt.Errorf("%s is stale; run `just tools-docs`", path))
		}
		return
	}
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
