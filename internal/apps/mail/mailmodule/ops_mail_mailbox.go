package mailmodule

import (
	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/apps/mail/application"
)

func mailMailboxOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{mailapp.DescribeDraftSend(), mailapp.DescribeDraftResolve(), mailapp.DescribeFolderCreate(), mailapp.DescribeMove(), mailapp.DescribeMessageArchive(), mailapp.DescribeMessageRead(), mailapp.DescribeMessageFlag()}, []application.Binding{
		{Kind: application.HTML, Target: "POST /messages/{message_id}/archive", Uses: []application.Name{mailapp.MessageArchiveName, mailapp.MessageName}},
		{Kind: application.HTML, Target: "POST /messages/{message_id}/read", Uses: []application.Name{mailapp.MessageReadName, mailapp.MessageName}},
		{Kind: application.HTML, Target: "POST /messages/{message_id}/flag", Uses: []application.Name{mailapp.MessageFlagName, mailapp.MessageName}},
		{Kind: application.HTML, Target: "POST /drafts/{draft_id}/send", Uses: []application.Name{mailapp.DraftSendName, mailapp.DraftName, mailapp.DraftsName}},
		{Kind: application.HTML, Target: "POST /drafts/{draft_id}/resolve", Uses: []application.Name{mailapp.DraftResolveName, mailapp.DraftName, mailapp.DraftsName}},
		{Kind: application.Tool, Target: "mail_folder_create", Uses: []application.Name{mailapp.FolderCreateName}},
		{Kind: application.Tool, Target: "mail_move", Uses: []application.Name{mailapp.MoveName}},
	}
}
