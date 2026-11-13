package mailmodule

import (
	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
)

func mailDraftOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{mailapp.DescribeDrafts(), mailapp.DescribeDraft(), mailapp.DescribeDraftCreate(), mailapp.DescribeDraftUpdate(), mailapp.DescribeDraftDiscard()}, []application.Binding{
		{Kind: application.HTML, Target: "GET /drafts", Uses: []application.Name{mailapp.DraftsName}},
		{Kind: application.HTML, Target: "GET /drafts/{draft_id}", Uses: []application.Name{mailapp.DraftName}},
		{Kind: application.HTML, Target: "GET /drafts/{draft_id}/card", Uses: []application.Name{mailapp.DraftName}},
		{Kind: application.HTML, Target: "POST /drafts/{draft_id}", Uses: []application.Name{mailapp.DraftUpdateName, mailapp.DraftName, mailapp.DraftsName}},
		{Kind: application.HTML, Target: "POST /drafts/{draft_id}/discard", Uses: []application.Name{mailapp.DraftDiscardName, mailapp.DraftName, mailapp.DraftsName}},
		{Kind: application.API, Target: "GET /drafts/{draft_id}", Uses: []application.Name{mailapp.DraftName}},
		{Kind: application.API, Target: "PATCH /drafts/{draft_id}", Uses: []application.Name{mailapp.DraftUpdateName, mailapp.DraftName}},
		{Kind: application.API, Target: "POST /drafts/{draft_id}/discard", Uses: []application.Name{mailapp.DraftDiscardName, mailapp.DraftName}},
		{Kind: application.Tool, Target: "draft_email_create", Uses: []application.Name{mailapp.DraftCreateName}},
		{Kind: application.Tool, Target: "draft_email_update", Uses: []application.Name{mailapp.DraftUpdateName}},
	}
}
