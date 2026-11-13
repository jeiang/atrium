package coremodule

import "github.com/jeiang/atrium/internal/application"

func finalBindings() []application.Binding {
	out := []application.Binding{
		{Kind: application.ProtocolOnly, Target: "GET /about", Reason: "static"},
		{Kind: application.ProtocolOnly, Target: "catalog operations", Reason: "coordinator"},
		{Kind: application.ProtocolOnly, Target: "catalog openapi", Reason: "coordinator"},
		{Kind: application.ProtocolOnly, Target: "recovery", Reason: "lifecycle"},
		{Kind: application.ProtocolOnly, Target: "embedding-index", Reason: "lifecycle"},
	}
	for _, path := range []string{"calendar", "export", "media", "notion"} {
		out = append(out, application.Binding{Kind: application.HTML, Target: "GET /settings/" + path, Uses: []application.Name{"settings.read"}})
	}
	return out
}
