package mailmodule

import (
	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
)

func mailMailboxOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{mailapp.DescribeDraftSend(), mailapp.DescribeDraftResolve(), mailapp.DescribeFolderCreate(), mailapp.DescribeMove()}, []application.Binding{
		{Kind: application.HTML, Target: "POST /drafts/{draft_id}/send", Uses: []application.Name{mailapp.DraftSendName, mailapp.DraftName, mailapp.DraftsName}},
		{Kind: application.HTML, Target: "POST /drafts/{draft_id}/resolve", Uses: []application.Name{mailapp.DraftResolveName, mailapp.DraftName, mailapp.DraftsName}},
		{Kind: application.API, Target: "POST /drafts/{draft_id}/send", Uses: []application.Name{mailapp.DraftSendName, mailapp.DraftName}},
		{Kind: application.API, Target: "POST /drafts/{draft_id}/resolve", Uses: []application.Name{mailapp.DraftResolveName, mailapp.DraftName}},
		{Kind: application.Tool, Target: "mail_folder_create", Uses: []application.Name{mailapp.FolderCreateName}},
		{Kind: application.Tool, Target: "mail_move", Uses: []application.Name{mailapp.MoveName}},
	}
}
