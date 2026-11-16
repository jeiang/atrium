package signalsmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/signals"
	"github.com/jeiang/atrium/internal/web"
)

type MailLoopsRequest struct {
	State  string     `query:"state" json:"-" api:"enum=owe_reply|waiting_on_them"`
	Cursor api.Cursor `query:"cursor" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
}
type MailLoopsPage struct {
	Items         []signals.Loop `json:"items"`
	NextCursor    *string        `json:"next_cursor" api:"nullable"`
	SnoozeDefault int            `json:"snooze_default"`
}
type MailCommitmentsRequest struct {
	View   string     `query:"view" json:"-" api:"enum=open|done|dismissed"`
	Cursor api.Cursor `query:"cursor" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
}
type MailCommitmentsPage struct {
	Items      []signals.Commitment `json:"items"`
	NextCursor *string              `json:"next_cursor" api:"nullable"`
}
type MailLoopRef struct {
	ID int64 `path:"loop_id" json:"-" api:"min=1"`
}
type MailLoopSnooze struct {
	ID   int64  `path:"loop_id" json:"-" api:"min=1"`
	Days *int64 `json:"days,omitempty"`
}
type MailLoopSnoozed struct {
	Days int64 `json:"days"`
}
type MailLoopsDismiss struct {
	State string `json:"state" api:"enum=owe_reply|waiting_on_them"`
}
type MailLoopsDismissed struct {
	Count int64 `json:"count"`
}
type MailCommitmentRef struct {
	ID int64 `path:"commitment_id" json:"-" api:"min=1"`
}

func servedRevision(name application.Name, data any) application.Revision {
	return application.Revision(state.Revision(string(name), nil, data, "operator"))
}

func offset(c api.Cursor) (int, error) {
	var n int
	if c.Set() {
		if err := c.Last(&n); err != nil {
			return 0, err
		}
		if n < 0 || n > int(^uint(0)>>1)-101 {
			return 0, &application.Error{Code: application.BadCursor, Message: "Invalid cursor"}
		}
	}
	return n, nil
}

func paged[T any](c api.Cursor, limit api.Limit, fetch func(int) ([]T, error)) ([]T, *string, error) {
	start, err := offset(c)
	if err != nil {
		return nil, nil, err
	}
	items, err := fetch(start + int(limit) + 1)
	if err != nil {
		return nil, nil, err
	}
	end := min(len(items), start+int(limit))
	var next *string
	if end < len(items) {
		next = c.Next(end)
	}
	return append([]T{}, items[min(start, len(items)):end]...), next, nil
}

func apiRoutes(d *web.Deps, service *Service) []application.Binding {
	svc := func() *Service { return service }
	modules := func(ctx context.Context) context.Context {
		if module.SetFrom(ctx) == nil {
			return module.WithSet(ctx, module.SetFrom(d.Ctx))
		}
		return ctx
	}
	const area = "mail"
	return []application.Binding{
		api.Query(d, DescribeOpenLoops(), application.APIShape{Area: area}, "GET /mail/loops", func(ctx context.Context, in MailLoopsRequest) (application.Read[MailLoopsPage], error) {
			var r application.Read[OpenLoopsResult]
			items, next, err := paged(in.Cursor, in.Limit, func(n int) ([]signals.Loop, error) {
				var err error
				r, err = svc().OpenLoops(ctx, OpenLoopsInput{State: in.State, Limit: n, Snooze: true})
				return r.Data.Loops, err
			})
			page := MailLoopsPage{Items: items, NextCursor: next, SnoozeDefault: r.Data.SnoozeDefault}
			return application.Read[MailLoopsPage]{Revision: servedRevision(OpenLoopsName, page), Data: page}, err
		}),
		api.Query(d, DescribeCommitments(), application.APIShape{Area: area}, "GET /mail/commitments", func(ctx context.Context, in MailCommitmentsRequest) (application.Read[MailCommitmentsPage], error) {
			var r application.Read[CommitmentsResult]
			items, next, err := paged(in.Cursor, in.Limit, func(n int) ([]signals.Commitment, error) {
				var err error
				r, err = svc().Commitments(ctx, CommitmentsInput{Status: in.View, Limit: n})
				return r.Data.Commitments, err
			})
			page := MailCommitmentsPage{Items: items, NextCursor: next}
			return application.Read[MailCommitmentsPage]{Revision: servedRevision(CommitmentsName, page), Data: page}, err
		}),
		api.Command(d, DescribeLoopSnooze(), application.APIShape{Area: area, Errors: []application.Code{application.NotFound}}, "POST /mail/loops/{loop_id}/snooze", func(ctx context.Context, in MailLoopSnooze) (application.Change[MailLoopSnoozed], error) {
			r, err := svc().LoopSnooze(ctx, LoopSnoozeInput{ID: in.ID, Days: in.Days})
			return application.Change[MailLoopSnoozed]{Data: MailLoopSnoozed{Days: r.Data.Days}, AuditID: r.AuditID}, err
		}),
		api.Command(d, DescribeLoopDismiss(), application.APIShape{Area: area, Body: application.BodyNone, Errors: []application.Code{application.NotFound}}, "POST /mail/loops/{loop_id}/dismiss", func(ctx context.Context, in MailLoopRef) (application.Change[struct{}], error) {
			return svc().LoopDismiss(ctx, LoopInput{ID: in.ID})
		}),
		api.Command(d, DescribeLoopsDismissMatching(), application.APIShape{Area: area, Errors: []application.Code{application.Invalid}}, "POST /mail/loops/dismiss", func(ctx context.Context, in MailLoopsDismiss) (application.Change[MailLoopsDismissed], error) {
			r, err := svc().LoopsDismissMatching(ctx, LoopsDismissMatchingInput{State: in.State})
			return application.Change[MailLoopsDismissed]{Data: MailLoopsDismissed{Count: r.Data.Count}, AuditID: r.AuditID}, err
		}),
		api.Command(d, DescribeCommitmentDone(), application.APIShape{Area: area, Body: application.BodyNone, Errors: []application.Code{application.NotFound, application.Conflict}}, "POST /mail/commitments/{commitment_id}/done", func(ctx context.Context, in MailCommitmentRef) (application.Change[struct{}], error) {
			return svc().CommitmentDone(modules(ctx), CommitmentInput{ID: in.ID})
		}),
		api.Command(d, DescribeCommitmentDismiss(), application.APIShape{Area: area, Body: application.BodyNone, Errors: []application.Code{application.NotFound, application.Conflict}}, "POST /mail/commitments/{commitment_id}/dismiss", func(ctx context.Context, in MailCommitmentRef) (application.Change[struct{}], error) {
			return svc().CommitmentDismiss(modules(ctx), CommitmentInput{ID: in.ID})
		}),
	}
}
