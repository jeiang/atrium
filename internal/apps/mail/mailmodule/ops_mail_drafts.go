package mailmodule

import (
	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/apps/mail/application"
)

func mailDraftOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{mailapp.DescribeDrafts(), mailapp.DescribeDraft(), mailapp.DescribeDraftCreate(), mailapp.DescribeDraftUpdate(), mailapp.DescribeDraftDiscard(), mailapp.DescribeDraftReply(), mailapp.DescribeDraftForward()}, []application.Binding{
		{Kind: application.Tool, Target: "draft_email_create", Uses: []application.Name{mailapp.DraftCreateName}},
		{Kind: application.Tool, Target: "draft_email_update", Uses: []application.Name{mailapp.DraftUpdateName}},
	}
}
