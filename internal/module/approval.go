package module

import (
	"slices"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/tool"
)

func (c *Catalog) ReachedVia(op Operation) application.Name {
	if !application.ApprovalOnly(op.Authority) || !c.approveTyped() {
		return ""
	}
	for _, b := range c.bindings {
		if b.Kind == application.Tool && slices.Contains(b.Uses, op.Name) && c.approvable(b.Target) {
			return application.ApproveOperation
		}
	}
	return ""
}

func (c *Catalog) approveTyped() bool {
	return slices.ContainsFunc(c.bindings, func(b Binding) bool {
		return b.Kind == application.API && b.API != nil && b.API.Area.Valid() && slices.Contains(b.Uses, application.ApproveOperation)
	})
}

func (c *Catalog) approvable(name string) bool {
	for _, d := range c.decls {
		for _, t := range d.Tools {
			if t.Tool.Name == name {
				return !t.Tool.Server && (t.Tool.Approval != nil || t.Tool.Effect == tool.Propose || t.Tool.Contexts.Has(tool.Chat))
			}
		}
	}
	return false
}
