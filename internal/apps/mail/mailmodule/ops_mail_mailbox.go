package mailmodule

import (
	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/apps/mail/application"
)

func mailMailboxOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{mailapp.DescribeDraftSend(), mailapp.DescribeDraftResolve(), mailapp.DescribeFolderCreate(), mailapp.DescribeMove(), mailapp.DescribeMessageArchive(), mailapp.DescribeMessageRead(), mailapp.DescribeMessageFlag()}, []application.Binding{
		{Kind: application.Tool, Target: "mail_folder_create", Uses: []application.Name{mailapp.FolderCreateName}},
		{Kind: application.Tool, Target: "mail_move", Uses: []application.Name{mailapp.MoveName}},
	}
}
