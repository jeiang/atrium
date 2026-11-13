package mediamodule

import "github.com/jeiang/atrium/internal/application"

func finalBindings() []application.Binding {
	return []application.Binding{
		{Kind: application.ProtocolOnly, Target: "GET /apps/media", Reason: "redirect"},
		{Kind: application.ProtocolOnly, Target: "GET /apps/media/add", Reason: "transport"},
		{Kind: application.ProtocolOnly, Target: "* /media", Reason: "redirect"},
		{Kind: application.ProtocolOnly, Target: "* /media/*", Reason: "redirect"},
		{Kind: application.ProtocolOnly, Target: "* /media/file/*", Reason: "redirect"},
		{Kind: application.ProtocolOnly, Target: "GET /media/file/*", Reason: "redirect"},
		{Kind: application.ProtocolOnly, Target: "HEAD /media/file/*", Reason: "redirect"},
		{Kind: application.ProtocolOnly, Target: "media-catalogue", Reason: "lifecycle"},
	}
}
