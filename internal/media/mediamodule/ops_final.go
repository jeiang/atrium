package mediamodule

import "github.com/jeiang/atrium/internal/application"

func finalBindings() []application.Binding {
	return []application.Binding{
		{Kind: application.ProtocolOnly, Target: "GET /apps/media", Reason: "redirect"},
		{Kind: application.ProtocolOnly, Target: "GET /apps/media/add", Reason: "transport"},
		{Kind: application.ProtocolOnly, Target: "* /media", Reason: "redirect"},
		{Kind: application.ProtocolOnly, Target: "* /media/*", Reason: "redirect"},
		{Kind: application.ProtocolOnly, Target: "* /media/file/*", Reason: "redirect"},
		{Kind: application.HTML, Target: "GET /media/file/*", Uses: []application.Name{"media.file"}},
		{Kind: application.HTML, Target: "HEAD /media/file/*", Uses: []application.Name{"media.file"}},
		{Kind: application.ProtocolOnly, Target: "media-catalogue", Reason: "lifecycle"},
	}
}
