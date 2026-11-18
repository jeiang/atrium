package coremodule

import (
	"cmp"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"sync"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant/permission"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
	"github.com/jeiang/atrium/internal/web"
)

type ProposalPayload interface{ proposalPayload() }

type PermissionPayload struct {
	Kind     string                   `json:"kind"`
	Args     []reviewapp.Arg          `json:"args"`
	ThreadID *int64                   `json:"thread_id" api:"nullable"`
	RunID    *int64                   `json:"run_id" api:"nullable"`
	Change   reviewapp.ProposalChange `json:"change" api:"nullable"`
}

type ActionPayload struct {
	Kind         string                   `json:"kind"`
	ProposalKind string                   `json:"proposal_kind"`
	Module       string                   `json:"module"`
	Label        string                   `json:"label"`
	Args         []reviewapp.Arg          `json:"args"`
	Preview      json.RawMessage          `json:"preview"`
	Change       reviewapp.ProposalChange `json:"change" api:"nullable"`
	Result       json.RawMessage          `json:"result"`
}

func (PermissionPayload) proposalPayload() {}
func (ActionPayload) proposalPayload()     {}

var registerPayload = sync.OnceFunc(func() {
	reviewapp.RegisterChanges()
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

type QueueRequest struct {
	Kind   string     `query:"kind" json:"-" api:"enum=ask|proposal|draft|attention"`
	Module string     `query:"module" json:"-"`
	Cursor api.Cursor `query:"cursor" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
}

type QueuePage struct {
	Items      []reviewapp.QueueItem `json:"items"`
	NextCursor *string               `json:"next_cursor" api:"nullable"`
	Total      int                   `json:"total"`
	Counts     reviewapp.QueueCounts `json:"counts"`
}

type QueueItemRequest struct {
	ItemID string `path:"item_id" json:"-"`
}

const queueTopics = "Refetch on an app event `change` for any of the topics review, effects, mail, attention, assistant, tasks, calendar, ops, settings and connections; the revision covers the result, so it also moves when an item becomes due or a snooze ends without an event."

const queueDescription = "The one inbox: everything that waits for the operator, in one order with one count. " +
	"`total` equals `needs_you` from `GET /shell`, and `total` and `counts` cover the whole queue whatever `kind`, `module`, `cursor` and `limit` are. " +
	"Order: asks that block the assistant (parked questions, permission requests) first, oldest first; then items with a due time, earliest due first; then everything else, oldest arrival first; ties break on `id`. " +
	"`module` keeps only the items of that module. Proposal items carry the proposal's `proposal_kind`. " +
	"Items of a module that is off are left out. An item's `id` is `<source.type>:<source.id>`. " + queueTopics

const queueItemDescription = "One queue item by `id`. `proposal:<proposal_id>` and `draft:<draft_id>` are the items behind `/proposals/{proposal_id}` and `/drafts/{draft_id}`. " +
	"Not found means the item no longer waits for the operator; read the proposal or draft for its outcome. " + queueTopics

func withModules(ctx context.Context, d *web.Deps) context.Context {
	if module.SetFrom(ctx) == nil {
		return module.WithSet(ctx, module.SetFrom(d.Ctx))
	}
	return ctx
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
		out.Payload = PermissionPayload{Kind: permission.Kind, Args: c.Args, ThreadID: c.ThreadID, RunID: c.RunID, Change: reviewapp.GuardedChangeOf(c)}
	} else {
		out.Payload = ActionPayload{Kind: "action", ProposalKind: c.Kind, Module: c.Module, Label: c.KindLabel, Args: c.Args, Preview: rawJSON(c.Preview), Change: reviewapp.ChangeOf(c), Result: rawJSON(c.Result)}
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
	out.Target = pathless(out.Target)
	return out
}

func pathless(s string) string {
	if filepath.IsAbs(s) {
		return filepath.Base(s)
	}
	return s
}

func reviewRoutes(d *web.Deps, svc func() *reviewapp.Service) []application.Binding {
	registerPayload()
	return append([]application.Binding{
		api.Query(d, reviewapp.DescribeOne(reviewapp.QueueName), application.APIShape{Area: "review", Description: queueDescription, Errors: []application.Code{application.Invalid}},
			"GET /queue", func(ctx context.Context, in QueueRequest) (application.Read[QueuePage], error) {
				input := reviewapp.QueueInput{Kind: in.Kind, Module: in.Module, Limit: int(in.Limit)}
				if in.Cursor.Set() {
					input.After = &reviewapp.QueueKey{}
					if err := in.Cursor.Last(input.After); err != nil {
						return application.Read[QueuePage]{}, err
					}
				}
				found, err := svc().Queue(withModules(ctx, d), input)
				if err != nil {
					return application.Read[QueuePage]{}, err
				}
				page := QueuePage{Items: found.Data.Items, Total: found.Data.Total, Counts: found.Data.Counts}
				if found.Data.Next != nil {
					page.NextCursor = in.Cursor.Next(found.Data.Next)
				}
				return application.Read[QueuePage]{Revision: found.Revision, Data: page}, nil
			}),
		api.Query(d, reviewapp.DescribeOne(reviewapp.QueueItemName), application.APIShape{Area: "review", Description: queueItemDescription, Errors: []application.Code{application.NotFound}},
			"GET /queue/{item_id}", func(ctx context.Context, in QueueItemRequest) (application.Read[reviewapp.QueueItem], error) {
				return svc().QueueItem(withModules(ctx, d), reviewapp.QueueItemInput{ID: in.ItemID})
			}),
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
					page.Items = append(page.Items, ProposalItem{ProposalSummary: p, Actions: proposalActions(p.Kind, p.Status, p.Undoable, p.Blocked)})
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
				var after *reviewapp.ActivityKey
				if in.Cursor.Set() {
					after = &reviewapp.ActivityKey{}
					if err := in.Cursor.Last(after); err != nil {
						return application.Read[ActivityPage]{}, err
					}
				}
				found, err := svc().Activity(ctx, reviewapp.ActivityInput{Category: in.Category, After: after, Limit: int(in.Limit), Operations: true, HidePrivate: true})
				if err != nil {
					return application.Read[ActivityPage]{}, err
				}
				page := ActivityPage{Items: []ActivityItem{}}
				for _, e := range found.Data.Entries {
					page.Items = append(page.Items, activityItemOf(e))
				}
				if found.Data.Next != nil {
					page.NextCursor = in.Cursor.Next(found.Data.Next)
				}
				return application.Read[ActivityPage]{Revision: found.Revision, Data: page}, nil
			}),
		api.Query(d, reviewapp.DescribeOne(reviewapp.PermissionsName), application.APIShape{Area: "permissions"},
			"GET /permissions", func(ctx context.Context, in reviewapp.PermissionsInput) (application.Read[reviewapp.Permissions], error) {
				return svc().Permissions(ctx, in)
			}),
		api.Command(d, reviewapp.DescribeOne(reviewapp.PermissionsUpdateName), application.APIShape{Area: "permissions", Errors: []application.Code{application.Invalid, application.Refused, application.Precondition}, Description: "Sets permission rules. If-Match is optional: send the ETag of GET /permissions to refuse the change with 412 when the rules or pending requests changed since it was read; without it the change applies as before."},
			"PUT /permissions", func(ctx context.Context, in reviewapp.PermissionsUpdateInput) (application.Change[reviewapp.PermissionsUpdated], error) {
				return svc().UpdatePermissions(ctx, in)
			}),
	}, reviewWireRoutes(d, svc)...)
}

func rawJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
