package mailmodule

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/mail"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/reviewpages"
	"github.com/jeiang/atrium/internal/module"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
	"github.com/jeiang/atrium/internal/web"
)

type DraftRequest struct {
	ID int64 `path:"draft_id" json:"-"`
}
type DraftUpdateRequest struct {
	ID       int64   `path:"draft_id" json:"-"`
	Account  *string `json:"account"`
	To       *string `json:"to"`
	Cc       *string `json:"cc"`
	Subject  *string `json:"subject"`
	Body     *string `json:"body"`
	Revision *int64  `json:"revision"`
	IfMatch  string  `header:"If-Match" json:"-"`
}
type DraftSendRequest struct {
	ID       int64  `path:"draft_id" json:"-"`
	Revision *int64 `json:"revision"`
	IfMatch  string `header:"If-Match" json:"-"`
}
type DraftResolveRequest struct {
	ID      int64  `path:"draft_id" json:"-"`
	Outcome string `json:"outcome" api:"enum=sent|draft"`
}
type DraftResponse struct {
	Draft reviewpages.Draft `json:"draft"`
}
type DraftSendResponse struct {
	Draft      reviewpages.Draft `json:"draft"`
	Filed      string            `json:"filed"`
	FiledError string            `json:"filed_error,omitempty"`
	Refused    []string          `json:"refused"`
}
type DraftsRequest struct {
	Status drafts.Status `query:"status" json:"-" api:"enum=draft|unknown|sent|discarded|sending"`
	Cursor api.Cursor    `query:"cursor" json:"-"`
	Limit  api.Limit     `query:"limit" json:"-"`
}
type DraftsResponse struct {
	Items      []reviewpages.Draft `json:"items"`
	NextCursor *string             `json:"next_cursor" api:"nullable"`
}

type draftAPI struct {
	mail   *mailapp.Service
	drafts *drafts.Store
}

func (s *draftAPI) draftRevision(ctx context.Context, id int64, body *int64, header string) (*int64, error) {
	if header == "" {
		return body, nil
	}
	tag := strings.Trim(strings.TrimPrefix(header, "W/"), "\"")
	n, err := strconv.ParseInt(tag, 10, 64)
	if err != nil {
		current, err := s.mail.Draft(ctx, mailapp.DraftInput{ID: id})
		if err != nil {
			return nil, err
		}
		if application.Revision(tag) != current.Revision {
			return nil, &application.Error{Code: application.Conflict, Reason: "stale", Message: "The draft changed since it was read."}
		}
		n = current.Data.Row.Revision
	}
	if n < 1 || (body != nil && *body != n) {
		return nil, &application.Error{Code: application.Invalid, Message: "If-Match must match the draft revision", Fields: []application.Field{{Name: "revision", Code: "invalid", Message: "Conflicting or invalid revision"}}}
	}
	return &n, nil
}
func draftFailure(err error, reason string, effects []int64) error {
	if err == nil {
		return nil
	}
	e := *application.Classify(err)
	if len(effects) > 0 {
		e.EffectIDs = effects
	}
	var disabled *module.OwnerDisabled
	if errors.As(err, &disabled) {
		e.Code, e.Reason = application.Conflict, "module_disabled"
		return &e
	}
	if reason != "" {
		e.Code, e.Reason = application.Conflict, reason
	}
	return &e
}
func mailDraftRoutes(d *web.Deps, m Module) []application.Binding {
	s := &draftAPI{}
	if d != nil {
		open := imapclient.Opener(d.Cfg)
		if m.Clients != nil {
			open = m.Clients(d.Cfg)
		}
		effects, err := module.SetFrom(d.Ctx).Catalog().Ledger(d.DB, d.Env, open, mail.WallClock)
		if err != nil {
			panic(err)
		}
		d.DrainOnStop(effects.Drain)
		s.drafts = &drafts.Store{DB: d.DB, Cfg: d.Cfg, Ledger: effects, Open: open, Now: mail.WallClock}
		s.mail = mailPageService(d).WithSender(s.drafts)
	}
	shape := func(t reflect.Type) application.APIShape { return application.APIShape{Area: "drafts", Response: t} }
	return []application.Binding{
		api.Query(d, mailapp.DescribeDraft(), shape(reflect.TypeFor[reviewpages.Draft]()), "GET /drafts/{draft_id}", func(ctx context.Context, in DraftRequest) (application.Read[reviewpages.Draft], error) {
			r, err := s.mail.Draft(ctx, mailapp.DraftInput{ID: in.ID})
			out := application.Read[reviewpages.Draft]{Revision: r.Revision}
			if err == nil {
				out.Data = s.currentDraft(ctx, in.ID)
			}
			return out, err
		}),
		api.Query(d, mailapp.DescribeDrafts(), shape(reflect.TypeFor[DraftsResponse]()), "GET /drafts", func(ctx context.Context, in DraftsRequest) (application.Read[DraftsResponse], error) {
			var offset int
			if in.Cursor.Set() {
				if err := in.Cursor.Last(&offset); err != nil {
					return application.Read[DraftsResponse]{}, err
				}
			}
			if offset < 0 || offset > int(^uint(0)>>1)-101 {
				return application.Read[DraftsResponse]{}, &application.Error{Code: application.BadCursor, Message: "Invalid cursor"}
			}
			r, err := s.mail.Drafts(ctx, mailapp.DraftsInput{Status: in.Status, Offset: offset, Limit: int(in.Limit) + 1})
			out := DraftsResponse{Items: []reviewpages.Draft{}}
			if err != nil {
				return application.Read[DraftsResponse]{}, err
			}
			cards := r.Data.Cards
			if len(cards) > int(in.Limit) {
				cards = cards[:int(in.Limit)]
				out.NextCursor = in.Cursor.Next(offset + len(cards))
			}
			wire := make([]reviewapp.DraftCard, len(cards))
			for i, c := range cards {
				wire[i] = reviewapp.DraftCard{Row: c.Row, Unexpected: c.Unexpected}
			}
			out.Items = reviewpages.DraftsOf(wire, reviewapp.AccountConfigured(ctx))
			return application.Read[DraftsResponse]{Revision: r.Revision, Data: out}, nil
		}),
		api.Command(d, mailapp.DescribeDraftUpdate(), shape(reflect.TypeFor[reviewpages.Draft]()), "PATCH /drafts/{draft_id}", func(ctx context.Context, in DraftUpdateRequest) (application.Change[reviewpages.Draft], error) {
			rev, err := s.draftRevision(ctx, in.ID, in.Revision, in.IfMatch)
			if err != nil {
				return application.Change[reviewpages.Draft]{}, err
			}
			fields := mailapp.DraftFields{Account: in.Account, Subject: in.Subject, Body: in.Body}
			if in.To != nil {
				fields.To = &mailapp.Recipients{Text: *in.To}
			}
			if in.Cc != nil {
				fields.Cc = &mailapp.Recipients{Text: *in.Cc}
			}
			r, err := s.mail.DraftUpdate(ctx, mailapp.DraftUpdateInput{ID: in.ID, Revision: rev, Fields: fields})
			out := application.Change[reviewpages.Draft]{AuditID: r.AuditID, EffectIDs: r.EffectIDs}
			if err != nil {
				e := *application.Classify(err)
				if e.Code == application.Conflict {
					e.Reason = "stale"
				}
				if e.Code == application.Invalid {
					e.Reason = "invalid_draft"
				}
				return out, draftFailure(&e, "", r.EffectIDs)
			}
			out.Data = s.currentDraft(ctx, in.ID)
			return out, nil
		}),
		api.Command(d, mailapp.DescribeDraftSend(), application.APIShape{Area: "drafts", Response: reflect.TypeFor[DraftSendResponse](), OptionalBody: true}, "POST /drafts/{draft_id}/send", func(ctx context.Context, in DraftSendRequest) (application.Change[DraftSendResponse], error) {
			rev, err := s.draftRevision(ctx, in.ID, in.Revision, in.IfMatch)
			if err != nil {
				return application.Change[DraftSendResponse]{}, err
			}
			if rev == nil {
				return application.Change[DraftSendResponse]{}, &application.Error{Code: application.Invalid, Message: "revision is required.", Fields: []application.Field{{Name: "revision", Code: "missing", Message: "Field required"}}}
			}
			if _, err := s.mail.Draft(ctx, mailapp.DraftInput{ID: in.ID}); err != nil {
				return application.Change[DraftSendResponse]{}, err
			}
			r, err := s.mail.DraftSend(ctx, mailapp.DraftSendInput{ID: in.ID, Revision: *rev})
			out := application.Change[DraftSendResponse]{AuditID: r.AuditID, EffectIDs: r.EffectIDs}
			if err != nil {
				reason := "not_sent"
				var unknown drafts.OutcomeUnknown
				if errors.As(err, &unknown) {
					reason = "send_outcome_unknown"
				} else if current, e := s.drafts.Get(ctx, in.ID); e == nil && current != nil && current.Status == drafts.Draft && current.Revision != *rev {
					reason = "stale"
				}
				return out, draftFailure(err, reason, r.EffectIDs)
			}
			result := r.Data.Sent
			refused := make([]string, len(result.Refused))
			for i, v := range result.Refused {
				refused[i] = v.Addr
			}
			out.Data = DraftSendResponse{Draft: s.currentDraft(ctx, in.ID), Filed: draftFiled(result.Filed), FiledError: result.FiledError, Refused: refused}
			return out, nil
		}),
		api.Command(d, mailapp.DescribeDraftDiscard(), application.APIShape{Area: "drafts", Body: application.BodyNone, Response: reflect.TypeFor[DraftResponse]()}, "POST /drafts/{draft_id}/discard", func(ctx context.Context, in DraftRequest) (application.Change[DraftResponse], error) {
			r, err := s.mail.DraftDiscard(ctx, mailapp.DraftInput{ID: in.ID})
			out := application.Change[DraftResponse]{AuditID: r.AuditID}
			if err != nil {
				e := application.Classify(err)
				reason := ""
				if e.Code == application.Invalid || e.Code == application.Conflict || e.Code == application.Refused {
					reason = "not_discarded"
				}
				return out, draftFailure(err, reason, nil)
			}
			out.Data.Draft = s.currentDraft(ctx, in.ID)
			return out, nil
		}),
		api.Command(d, mailapp.DescribeDraftResolve(), shape(reflect.TypeFor[DraftResponse]()), "POST /drafts/{draft_id}/resolve", func(ctx context.Context, in DraftResolveRequest) (application.Change[DraftResponse], error) {
			if _, err := s.mail.Draft(ctx, mailapp.DraftInput{ID: in.ID}); err != nil {
				return application.Change[DraftResponse]{}, err
			}
			r, err := s.mail.DraftResolve(ctx, mailapp.DraftResolveInput{ID: in.ID, Outcome: in.Outcome})
			out := application.Change[DraftResponse]{AuditID: r.AuditID, EffectIDs: r.EffectIDs}
			if err != nil {
				return out, draftFailure(err, "not_resolved", r.EffectIDs)
			}
			out.Data.Draft = s.currentDraft(ctx, in.ID)
			return out, nil
		}),
	}
}

func (s *draftAPI) currentDraft(ctx context.Context, id int64) reviewpages.Draft {
	found, err := s.mail.Draft(ctx, mailapp.DraftInput{ID: id})
	if err != nil {
		panic(err)
	}
	return reviewpages.DraftsOf([]reviewapp.DraftCard{{Row: found.Data.Row, Unexpected: found.Data.Unexpected}}, reviewapp.AccountConfigured(ctx))[0]
}

func draftFiled(f drafts.Filed) string {
	switch f {
	case drafts.FiledCopy:
		return "copy"
	case drafts.FiledByServer:
		return "server"
	case drafts.FiledNoMailbox:
		return "no_mailbox"
	case drafts.FiledFailed:
		return "failed"
	}
	return "none"
}
