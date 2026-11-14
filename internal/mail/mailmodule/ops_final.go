package mailmodule

import "github.com/jeiang/atrium/internal/application"

func finalBindings() []application.Binding {
	return []application.Binding{
		{Kind: application.Service, Target: "mail/watch", Uses: []application.Name{"mail.sync"}},
		{Kind: application.HTML, Target: "POST /settings/mail/settings", Uses: []application.Name{"settings.set", "mail.accounts"}, Delegates: []application.Name{"settings.set"}},
		{Kind: application.HTML, Target: "POST /live/actions/{action_id}/undo", Uses: []application.Name{"review.undo"}, Delegates: []application.Name{"review.undo"}},
		{Kind: application.CLI, Target: "live undo", Uses: []application.Name{"review.undo"}, Delegates: []application.Name{"review.undo"}},
		{Kind: application.CLI, Target: "live resolve", Uses: []application.Name{"review.resolve"}, Delegates: []application.Name{"review.resolve"}},
	}
}
