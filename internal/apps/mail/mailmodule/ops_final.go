package mailmodule

import "github.com/jeiang/atrium/internal/application"

func finalBindings() []application.Binding {
	return []application.Binding{
		{Kind: application.Service, Target: "mail/watch", Uses: []application.Name{"mail.sync"}},
		{Kind: application.CLI, Target: "live undo", Uses: []application.Name{"review.undo"}, Delegates: []application.Name{"review.undo"}},
		{Kind: application.CLI, Target: "live resolve", Uses: []application.Name{"review.resolve"}, Delegates: []application.Name{"review.resolve"}},
	}
}
