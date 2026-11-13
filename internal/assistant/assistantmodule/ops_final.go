package assistantmodule

import "github.com/jeiang/atrium/internal/application"

func finalBindings() []application.Binding {
	return []application.Binding{
		{Kind: application.ProtocolOnly, Target: "GET /settings", Reason: "redirect"},
		{Kind: application.ProtocolOnly, Target: "GET /settings/permissions", Reason: "redirect"},
		{Kind: application.HTML, Target: "POST /settings/assistant", Uses: []application.Name{"settings.set", "assistant.configuration"}, Delegates: []application.Name{"settings.set"}},
		{Kind: application.HTML, Target: "POST /settings/permissions", Uses: []application.Name{"settings.set", "assistant.configuration"}, Delegates: []application.Name{"settings.set"}},
		{Kind: application.ProtocolOnly, Target: "agent tick", Reason: "coordinator"},
		{Kind: application.ProtocolOnly, Target: "web_search", Reason: "native-tool"},
		{Kind: application.ProtocolOnly, Target: "report", Reason: "transport"},
		{Kind: application.ProtocolOnly, Target: "assistant", Reason: "lifecycle"},
	}
}
