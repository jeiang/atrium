package coremodule

import (
	"cmp"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"sync"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant/permission"
	"github.com/jeiang/atrium/internal/proposals"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
	"github.com/jeiang/atrium/internal/web"
)

type ProposalPayload interface{ proposalPayload() }

type PermissionPayload struct {
	Kind     string          `json:"kind"`
	Args     []reviewapp.Arg `json:"args"`
	ThreadID *int64          `json:"thread_id" api:"nullable"`
	RunID    *int64          `json:"run_id" api:"nullable"`
}

type ActionPayload struct {
	Kind         string          `json:"kind"`
	ProposalKind string          `json:"proposal_kind"`
	Module       string          `json:"module"`
	Label        string          `json:"label"`
	Args         []reviewapp.Arg `json:"args"`
	Preview      json.RawMessage `json:"preview"`
	Result       json.RawMessage `json:"result"`
}

func (PermissionPayload) proposalPayload() {}
func (ActionPayload) proposalPayload()     {}

var registerPayload = sync.OnceFunc(func() {
	application.RegisterUnion[ProposalPayload](map[string]reflect.Type{
		permission.Kind: reflect.TypeFor[PermissionPayload](),
		"action":        reflect.TypeFor[ActionPayload](),
	})
})

type ProposalsRequest struct {
	Status string     `query:"status" json:"-"`
	Kind   string     `query:"kind" json:"-"`
	Cursor api.Cursor `query:"cursor" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
}

type ProposalItem struct {
	reviewapp.ProposalSummary
	Actions []string `json:"actions"`
}

type ProposalsPage struct {
	Items      []ProposalItem `json:"items"`
	NextCursor *string        `json:"next_cursor" api:"nullable"`
}

type ProposalRequest struct {
	ProposalID int64 `path:"proposal_id" json:"-"`
}

type ProposalDetail struct {
	ID         int64           `json:"id"`
	Kind       string          `json:"kind"`
	Status     string          `json:"status"`
	Account    *string         `json:"account" api:"nullable"`
	Title      string          `json:"title"`
	CreatedAt  float64         `json:"created_at"`
	DecidedAt  *float64        `json:"decided_at" api:"nullable"`
	AppliedAt  *float64        `json:"applied_at" api:"nullable"`
	UndoneAt   *float64        `json:"undone_at" api:"nullable"`
	Undoable   bool            `json:"undoable"`
	Blocked    bool            `json:"blocked"`
	LinkThread *int64          `json:"link_thread_id" api:"nullable"`
	Payload    ProposalPayload `json:"payload"`
	Actions    []string        `json:"actions"`
}

type ActivityRequest struct {
	Category string     `query:"category" json:"-"`
	Cursor   api.Cursor `query:"cursor" json:"-"`
	Limit    api.Limit  `query:"limit" json:"-"`
}

type ActivityItem struct {
	Source           string  `json:"source"`
	ID               int64   `json:"id"`
	Category         string  `json:"category"`
	TS               float64 `json:"ts"`
	Operation        string  `json:"operation"`
	Actor            string  `json:"actor"`
	Origin           string  `json:"origin"`
	Outcome          string  `json:"outcome"`
	EffectiveOutcome string  `json:"effective_outcome"`
	Target           string  `json:"target"`
	ThreadID         *int64  `json:"thread_id" api:"nullable"`
	CommandID        *string `json:"command_id" api:"nullable"`
	EffectIDs        []int64 `json:"effect_ids"`
}

type ActivityPage struct {
	Items      []ActivityItem `json:"items"`
	NextCursor *string        `json:"next_cursor" api:"nullable"`
}

type activityMark struct {
	Offset int     `json:"o"`
	TS     float64 `json:"t"`
	Source string  `json:"s"`
	ID     int64   `json:"i"`
}

func proposalActions(kind, status string, undoable, blocked bool) []string {
	out := []string{}
	switch {
	case blocked:
		out = append(out, "resolve")
	case status == proposals.Pending && kind != permission.Kind:
		out = append(out, "approve", "deny")
	case status == proposals.Applied && undoable:
		out = append(out, "undo")
	}
	return out
}

func detailOf(c reviewapp.Card) ProposalDetail {
	blocked := c.Blocked || c.BlockedEffect || c.BlockedUndo
	out := ProposalDetail{
		ID: c.ID, Kind: c.Kind, Status: c.Status, Account: c.Account, Title: c.Title, CreatedAt: c.CreatedAt, DecidedAt: c.DecidedAt,
		AppliedAt: c.AppliedAt, UndoneAt: c.UndoneAt, Undoable: c.Undoable, Blocked: blocked, LinkThread: c.LinkThread,
		Actions: proposalActions(c.Kind, c.Status, c.Undoable, blocked),
	}
	if c.Kind == permission.Kind {
		out.Payload = PermissionPayload{Kind: permission.Kind, Args: c.Args, ThreadID: c.ThreadID, RunID: c.RunID}
	} else {
		out.Payload = ActionPayload{Kind: "action", ProposalKind: c.Kind, Module: c.Module, Label: c.KindLabel, Args: c.Args, Preview: rawJSON(c.Preview), Result: rawJSON(c.Result)}
	}
	return out
}

func activityItemOf(e reviewapp.ActivityEntry) ActivityItem {
	out := ActivityItem{Source: e.Source, ID: e.ID, Category: e.Category, TS: e.TS, EffectIDs: []int64{}}
	switch {
	case e.Operation != nil:
		op := e.Operation
		out.Operation, out.Actor, out.Origin, out.Outcome, out.EffectiveOutcome = op.Name, op.Actor, op.Source, op.Outcome, op.Outcome
		out.Target, out.ThreadID, out.CommandID, out.EffectIDs = op.Target, op.ThreadID, op.CommandID, op.EffectIDs
	case e.Write != nil:
		out.Operation, out.Origin, out.Outcome, out.EffectiveOutcome = e.Write.Tool, "effect", e.Write.State, e.Write.Effective
		out.Target, out.ThreadID, out.EffectIDs = e.Write.Target, e.Write.ThreadID, []int64{e.ID}
	case e.Mail != nil:
		out.Operation, out.Origin, out.Outcome = "mail."+e.Mail.Kind, e.Mail.Account, e.Mail.State
		out.EffectiveOutcome = cmp.Or(e.Mail.EffectiveOutcome, e.Mail.State)
		out.Target = "message:" + strconv.FormatInt(e.Mail.MessageID, 10)
	case e.Phone != nil:
		out.Operation, out.Actor, out.Origin, out.Outcome, out.EffectiveOutcome = "phone."+e.Phone.Kind, e.Phone.Actor, "phone", e.Phone.Status, e.Phone.Status
		out.Target = e.Phone.Target
	case e.Settings != nil:
		out.Operation, out.Actor, out.Origin, out.Outcome, out.EffectiveOutcome = "settings.set", e.Settings.Actor, e.Settings.Origin, "ok", "ok"
		out.Target = e.Settings.Tool
	}
	return out
}

func after(e reviewapp.ActivityEntry, m activityMark) bool {
	switch {
	case e.TS != m.TS:
		return e.TS < m.TS
	case e.ID != m.ID:
		return e.ID < m.ID
	}
	return e.Source != m.Source
}

func reviewRoutes(d *web.Deps, svc func() *reviewapp.Service) []application.Binding {
	registerPayload()
	return []application.Binding{
		api.Query(d, reviewapp.DescribeOne(reviewapp.ProposalsName), application.APIShape{Area: "proposals", Errors: []application.Code{application.Invalid}},
			"GET /proposals", func(ctx context.Context, in ProposalsRequest) (application.Read[ProposalsPage], error) {
				var before int64
				if in.Cursor.Set() {
					if err := in.Cursor.Last(&before); err != nil {
						return application.Read[ProposalsPage]{}, err
					}
				}
				found, err := svc().Proposals(ctx, reviewapp.ProposalsInput{Status: in.Status, Kind: in.Kind})
				if err != nil {
					return application.Read[ProposalsPage]{}, err
				}
				list := slices.SortedFunc(slices.Values(found.Data), func(a, b reviewapp.ProposalSummary) int { return cmp.Compare(b.ID, a.ID) })
				page := ProposalsPage{Items: []ProposalItem{}}
				for _, p := range list {
					if before != 0 && p.ID >= before {
						continue
					}
					if len(page.Items) == int(in.Limit) {
						page.NextCursor = in.Cursor.Next(page.Items[len(page.Items)-1].ID)
						break
					}
					page.Items = append(page.Items, ProposalItem{ProposalSummary: p, Actions: proposalActions(p.Kind, p.Status, p.Undoable, false)})
				}
				return application.Read[ProposalsPage]{Revision: found.Revision, Data: page}, nil
			}),
		api.Query(d, reviewapp.DescribeOne(reviewapp.ProposalName), application.APIShape{Area: "proposals", Errors: []application.Code{application.NotFound}},
			"GET /proposals/{proposal_id}", func(ctx context.Context, in ProposalRequest) (application.Read[ProposalDetail], error) {
				found, err := svc().Proposal(ctx, reviewapp.ProposalInput{ID: in.ProposalID})
				return application.Read[ProposalDetail]{Revision: found.Revision, Data: detailOf(found.Data)}, err
			}),
		api.Query(d, reviewapp.DescribeOne(reviewapp.ActivityName), application.APIShape{Area: "activity", Errors: []application.Code{application.Invalid}},
			"GET /activity", func(ctx context.Context, in ActivityRequest) (application.Read[ActivityPage], error) {
				var mark activityMark
				if in.Cursor.Set() {
					if err := in.Cursor.Last(&mark); err != nil {
						return application.Read[ActivityPage]{}, err
					}
				}
				found, err := svc().Activity(ctx, reviewapp.ActivityInput{Category: in.Category, Offset: mark.Offset, Limit: int(in.Limit), Operations: true, HidePrivate: true})
				if err != nil {
					return application.Read[ActivityPage]{}, err
				}
				page := ActivityPage{Items: []ActivityItem{}}
				last := mark
				for _, e := range found.Data.Entries {
					if in.Cursor.Set() && !after(e, mark) {
						continue
					}
					page.Items = append(page.Items, activityItemOf(e))
					last = activityMark{TS: e.TS, Source: e.Source, ID: e.ID}
				}
				if found.Data.NextOffset > 0 {
					last.Offset = found.Data.NextOffset
					page.NextCursor = in.Cursor.Next(last)
				}
				return application.Read[ActivityPage]{Revision: found.Revision, Data: page}, nil
			}),
		api.Query(d, reviewapp.DescribeOne(reviewapp.PermissionsName), application.APIShape{Area: "permissions"},
			"GET /permissions", func(ctx context.Context, in reviewapp.PermissionsInput) (application.Read[reviewapp.Permissions], error) {
				return svc().Permissions(ctx, in)
			}),
		api.Command(d, reviewapp.DescribeOne(reviewapp.PermissionsUpdateName), application.APIShape{Area: "permissions", Errors: []application.Code{application.Invalid, application.Refused}},
			"PUT /permissions", func(ctx context.Context, in reviewapp.PermissionsUpdateInput) (application.Change[reviewapp.PermissionsUpdated], error) {
				return svc().UpdatePermissions(ctx, in)
			}),
	}
}

func rawJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
