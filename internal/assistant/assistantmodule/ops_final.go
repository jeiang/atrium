package assistantmodule

import (
	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	settingsapp "github.com/jeiang/atrium/internal/settings/application"
)

func finalBindings() []application.Binding {
	return []application.Binding{
		{Kind: application.ProtocolOnly, Target: "agent tick", Reason: "coordinator"},
		{Kind: application.ProtocolOnly, Target: "web_search", Reason: "native-tool"},
		{Kind: application.ProtocolOnly, Target: "report", Reason: "transport"},
		{Kind: application.ProtocolOnly, Target: "assistant", Reason: "lifecycle"},
	}
}
