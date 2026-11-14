package coremodule

import (
	"cmp"
	"slices"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/web"
)

func catalogBindings(cat *module.Catalog) []module.Binding {
	out := cat.Bindings()
	for _, b := range web.BuiltinBindings(true) {
		out = append(out, module.Binding{Module: module.Core, Binding: b})
	}
	return out
}

func catalogProtocols(cat *module.Catalog) []catalogProtocol {
	var out []catalogProtocol
	for _, b := range catalogBindings(cat) {
		if b.Kind == application.ProtocolOnly {
			out = append(out, catalogProtocol{Module: b.Module, Target: b.Target, Reason: b.Reason})
		}
	}
	for _, target := range ops.ProtocolRoutes() {
		out = append(out, catalogProtocol{Module: module.Core, Listener: "ops", Target: target, Reason: "transport"})
	}
	slices.SortFunc(out, func(a, b catalogProtocol) int {
		return cmp.Or(cmp.Compare(a.Module, b.Module), cmp.Compare(a.Listener, b.Listener), cmp.Compare(a.Target, b.Target))
	})
	return out
}
