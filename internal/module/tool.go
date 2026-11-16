package module

import (
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/tool"
)

type ToolDefinition struct {
	Tool    tool.Tool
	Bind    registry.Binder
	Collect func(kinds []proposals.Definition) ToolDefinition
}

func collectKinds(decls []Declaration) ([]Declaration, []proposals.Definition) {
	var kinds []proposals.Definition
	for _, d := range decls {
		kinds = append(kinds, d.ProposeKinds...)
	}
	slices.SortFunc(kinds, func(a, b proposals.Definition) int { return strings.Compare(a.Kind.Name, b.Kind.Name) })
	out := slices.Clone(decls)
	for i, d := range out {
		if !slices.ContainsFunc(d.Tools, func(t ToolDefinition) bool { return t.Collect != nil }) {
			continue
		}
		tools := slices.Clone(d.Tools)
		for j, def := range tools {
			if def.Collect != nil {
				tools[j] = def.Collect(slices.Clone(kinds))
			}
		}
		out[i].Tools = tools
	}
	return out, kinds
}
