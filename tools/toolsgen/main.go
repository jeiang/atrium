package main

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/toolset"
	"github.com/jeiang/atrium/internal/settings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: toolsgen OUTPUT_FILE")
		os.Exit(2)
	}
	if err := generate(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "toolsgen:", err)
		os.Exit(1)
	}
}

type row struct {
	name    string
	kinds   []tool.Kind
	effect  tool.Effect
	flags   tool.Flags
	disable bool
}

func generate(path string) error {
	reg, err := toolset.Registry()
	if err != nil {
		return err
	}
	all, err := reg.All()
	if err != nil {
		return err
	}
	var rows []row
	for _, t := range all {
		var kinds []tool.Kind
		for _, k := range tool.AllKinds {
			if registry.Offered(k, t) {
				kinds = append(kinds, k)
			}
		}
		rows = append(rows, row{t.Name, kinds, t.Effect, t.Flags, canDisable(t.Name)})
	}
	rows = append(rows, row{
		registry.WebSearchName, append(slices.Clone(registry.WebSearchKinds), tool.Sub), tool.Read,
		registry.WebSearchFlags, canDisable(registry.WebSearchName),
	})
	slices.SortFunc(rows, func(a, b row) int { return strings.Compare(a.name, b.name) })
	var b strings.Builder
	b.WriteString("# Assistant tools\n\n")
	b.WriteString("Generated from the production tool registry by `just toolsgen`. Do not edit by hand.\n\n")
	b.WriteString("Run kinds are those that offer the tool after the run-kind policy. A tool that has an `enabled` setting stays listed when the operator turns it off. The flags follow the Rule of Two: untrusted output (U), private data (P) and external effect (E). A thread that read private data loses its external-effect tools.\n\n")
	b.WriteString("| Tool | Run kinds | Effect | U | P | E | Can be disabled |\n|---|---|---|---|---|---|---|\n")
	for _, r := range rows {
		kinds := make([]string, len(r.kinds))
		for i, k := range r.kinds {
			kinds[i] = string(k)
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s | %s |\n", r.name, strings.Join(kinds, ", "), r.effect,
			mark(r.flags.Has(tool.UntrustedOutput)), mark(r.flags.Has(tool.PrivateData)), mark(r.flags.Has(tool.ExternalEffect)), mark(r.disable))
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func canDisable(name string) bool {
	_, ok := settings.Default.Lookup(assistant.ToolEnabledKey(name))
	return ok
}

func mark(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
