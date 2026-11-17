package mailmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/apps/mail"
	"github.com/jeiang/atrium/internal/apps/mail/drafts"
	"github.com/jeiang/atrium/internal/apps/mail/tracking"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
)

func Refs() []module.RefKind {
	return []module.RefKind{
		module.NumberedRef("mail", func(ctx context.Context, q db.Querier, id int64, number string) (*module.Ref, error) {
			label, err := mail.Subject(ctx, q, id)
			return &module.Ref{Label: label, Line: "About mail message " + number + "."}, err
		}),
		module.NumberedRef("package", func(ctx context.Context, q db.Querier, id int64, number string) (*module.Ref, error) {
			label, err := tracking.Label(ctx, q, id)
			return &module.Ref{Label: label, Line: "About tracked package " + number + "."}, err
		}),
	}
}

func draftAttachment() module.Attachment {
	return module.Attachment{Kind: "draft", Tool: "draft_email_create", Key: "draft_id", Load: drafts.DictsForThread}
}
