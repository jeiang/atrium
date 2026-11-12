package calendartools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant/approval"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/calendar/candidates"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/tool"
)

const timeNote = "ISO-8601 (2026-10-16T09:00 or 2026-10-16); times without an offset are in the operator's timezone."

var eventProperties = `"calendar":{"type":"string","enum":["icloud","google"]},"title":{"type":"string"},` +
	`"start":{"type":"string","description":"` + timeNote + `"},` +
	`"end":{"type":"string","description":"Exclusive end, same format. Default: one hour (all-day: one day) after start."},` +
	`"all_day":{"type":"boolean","description":"start/end are dates"},"location":{"type":"string"},"notes":{"type":"string"}`

func guard(ctx context.Context, q db.Querier, target calendar.Target) error {
	id, err := ledger.BlockedTarget(ctx, q, string(target)+":")
	if err != nil {
		return err
	}
	if id != 0 {
		return tool.Error(fmt.Sprintf("calendar write %d has an unknown outcome; resolve it with `atrium effects resolve %d --outcome applied` or on the Activity page before writing to the %s calendar again", id, id, target))
	}
	return nil
}

func wrap(err error) error {
	var missing calendar.NotConfigured
	var failed calendar.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &missing):
		return tool.NotConfigured(missing.Error())
	case errors.As(err, &failed):
		return tool.Error(failed.Error())
	}
	var raised *tool.Raised
	if errors.As(err, &raised) {
		return err
	}
	return err
}

func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case string:
		return v != ""
	case bool:
		return v
	case int64:
		return v != 0
	case float64:
		return v != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

func optionalText(args tool.Args, name string) (any, error) {
	text, ok, err := tool.TextArg(args, name, false)
	if err != nil || !ok {
		return nil, err
	}
	return text, nil
}

func requiredText(args tool.Args, name string) (any, error) {
	text, ok, err := tool.TextArg(args, name, true)
	if err != nil || !ok {
		return nil, err
	}
	return text, nil
}

func list(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	start, err := requiredText(args, "start")
	if err != nil {
		return nil, err
	}
	end, err := requiredText(args, "end")
	if err != nil {
		return nil, err
	}
	target, _, err := tool.TextArg(args, "calendar", false)
	if err != nil {
		return nil, err
	}
	filter, err := calendar.ParseFilter(target)
	if err != nil {
		return nil, wrap(err)
	}
	set, err := calendar.LoadSettings(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	listing, err := calendar.ListEvents(ctx, tc.Env, set, start, end, filter)
	if err != nil {
		return nil, wrap(err)
	}
	if len(listing.Events) == 0 {
		return tool.Marked{Value: listing.Dict(), Marks: 0}, nil
	}
	return tool.Marked{Value: listing.Dict(), Marks: tool.Messages | tool.Untrusted}, nil
}

func remove(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	uid, _, err := tool.TextArg(args, "uid", true)
	if err != nil {
		return nil, err
	}
	row, err := calendar.FindTracked(ctx, tc.DB.Write, uid)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, tool.Error("only events created through atrium can be deleted, and this uid is not one of them")
	}
	var stored any
	if err := json.Unmarshal([]byte(row.Spec), &stored); err != nil {
		return nil, err
	}
	set, err := calendar.LoadSettings(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	rowTarget, err := calendar.ParseTarget(row.Calendar)
	if err != nil {
		return nil, wrap(err)
	}
	if err := guard(ctx, tc.DB.Read, rowTarget); err != nil {
		return nil, err
	}
	target := row.Calendar + ":" + uid
	before := jsonOf(map[string]any{"calendar": row.Calendar, "spec": stored})
	undo := jsonOf(recreateUndo{Op: "calendar_recreate", Calendar: row.Calendar, Spec: stored})
	ticket, err := tc.Ledger.Begin(ctx, ledger.New{
		Origin: "calendar", Operation: "calendar_delete", OperationKey: "calendar_delete:" + ledger.NewKey(),
		Target: target, ThreadID: &tc.ThreadID, RunID: &tc.RunID, ProposalID: tc.ProposalID, Before: before, Undo: undo,
		Resources: []ledger.Resource{{Key: "calendar:" + target}},
	}, nil)
	if err != nil {
		return nil, err
	}
	if err := calendar.DeleteEvent(ctx, tc.Env, set, rowTarget, uid); err != nil {
		state := ledger.Failed
		if calendar.OutcomeUnknown(err) {
			state = ledger.Unknown
		}
		_ = tc.Ledger.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: state, Target: target, Before: before, Error: err.Error()}, nil)
		return nil, wrap(err)
	}
	err = tc.Ledger.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{
		State: ledger.Done, Target: target, Before: before, Undo: undo,
	}, func(ctx context.Context, tx *sql.Tx) error {
		return calendar.Untrack(ctx, tx, uid, tc.Now())
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"deleted": uid, "calendar": row.Calendar}, nil
}

func propose(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	raw := map[string]any{}
	for _, name := range []string{"title", "start", "end", "location", "notes"} {
		v, err := optionalText(args, name)
		if err != nil {
			return nil, err
		}
		raw[name] = v
	}
	flag, _ := args.Get("all_day")
	target, _ := args.Get("calendar")
	raw["all_day"], raw["calendar"] = truthy(flag), target
	source := map[string]any{}
	if _, hasID := args.Get("source_message_id"); hasID {
		messageID, _, err := tool.IntArg(args, "source_message_id", true)
		if err != nil {
			return nil, err
		}
		source["message_id"] = messageID
	}
	proposals := &calendar.Proposals{DB: tc.DB, Now: tc.Now, Env: tc.Env}
	id, created, err := proposals.Propose(ctx, raw, source, "")
	var refused calendar.ProposalError
	if errors.As(err, &refused) {
		return nil, tool.Error(refused.Error())
	}
	if err != nil {
		return nil, wrap(err)
	}
	if !created {
		return map[string]any{"proposed": false, "reason": "an identical proposal already exists"}, nil
	}
	if stored, ok := source["message_id"]; ok {
		messageID, _ := stored.(int64)
		if _, err := candidates.Drafted(ctx, tc.DB, messageID, tc.Now()); err != nil {
			return nil, err
		}
	}
	return map[string]any{"proposed": true, "proposal_id": id}, nil
}

type deleteUndo struct {
	Op       string `json:"op"`
	Calendar string `json:"calendar"`
	UID      string `json:"uid"`
}

type recreateUndo struct {
	Op       string `json:"op"`
	Calendar string `json:"calendar"`
	Spec     any    `json:"spec"`
}

func jsonOf(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func undoDelete(store *ledger.Store, env config.Env) ledger.Undoer {
	return ledger.Undoer{
		Validate: func(raw json.RawMessage) error {
			var u deleteUndo
			if err := json.Unmarshal(raw, &u); err != nil || u.UID == "" {
				return ledger.Refused("calendar undo payload is invalid")
			}
			return nil
		},
		External: func(ctx context.Context, raw json.RawMessage) (ledger.Result, error) {
			var u deleteUndo
			if err := json.Unmarshal(raw, &u); err != nil {
				return ledger.Result{}, ledger.Refused("calendar undo payload is invalid")
			}
			target, err := calendar.ParseTarget(u.Calendar)
			if err != nil {
				return ledger.Result{}, ledger.Refused(err.Error())
			}
			uid := u.UID
			set, err := calendar.LoadSettings(ctx, store.DB.Read)
			if err != nil {
				return ledger.Result{}, err
			}
			if err := calendar.DeleteEvent(ctx, env, set, target, uid); err != nil {
				var failed calendar.Error
				if errors.As(err, &failed) {
					return ledger.Result{State: ledger.Failed, Error: failed.Error()}, nil
				}
				return ledger.Result{}, err
			}
			return ledger.Result{State: ledger.Done, After: jsonOf(map[string]any{"deleted": uid})}, nil
		},
		Finalize: func(ctx context.Context, tx *sql.Tx, raw json.RawMessage, result ledger.Result) error {
			if result.State != ledger.Done {
				return nil
			}
			var u deleteUndo
			if err := json.Unmarshal(raw, &u); err != nil {
				return err
			}
			return calendar.Untrack(ctx, tx, u.UID, nowSeconds())
		},
	}
}

func undoRecreate(store *ledger.Store, env config.Env) ledger.Undoer {
	return ledger.Undoer{
		Validate: func(raw json.RawMessage) error {
			var u recreateUndo
			if err := json.Unmarshal(raw, &u); err != nil || u.Calendar == "" || u.Spec == nil {
				return ledger.Refused("calendar undo payload is invalid")
			}
			return nil
		},
		External: func(ctx context.Context, raw json.RawMessage) (ledger.Result, error) {
			var u recreateUndo
			if err := json.Unmarshal(raw, &u); err != nil {
				return ledger.Result{}, ledger.Refused("calendar undo payload is invalid")
			}
			target, err := calendar.ParseTarget(u.Calendar)
			if err != nil {
				return ledger.Result{}, ledger.Refused(err.Error())
			}
			restored, err := calendar.SpecOf(u.Spec)
			if err != nil {
				return ledger.Result{}, ledger.Refused(err.Error())
			}
			restored.UID = calendar.NewUID()
			tz, err := calendar.Zone(ctx, env)
			if err != nil {
				return ledger.Result{}, err
			}
			set, err := calendar.LoadSettings(ctx, store.DB.Read)
			if err != nil {
				return ledger.Result{}, err
			}
			if _, err := calendar.CreateEvent(ctx, env, set, target, restored, tz); err != nil {
				var failed calendar.Error
				if errors.As(err, &failed) {
					return ledger.Result{State: ledger.Failed, Error: failed.Error()}, nil
				}
				return ledger.Result{}, err
			}
			return ledger.Result{State: ledger.Done, After: jsonOf(map[string]any{"created": restored.UID, "spec": restored.Dict()})}, nil
		},
		Finalize: func(ctx context.Context, tx *sql.Tx, raw json.RawMessage, result ledger.Result) error {
			if result.State != ledger.Done {
				return nil
			}
			var u recreateUndo
			if err := json.Unmarshal(raw, &u); err != nil {
				return err
			}
			target, err := calendar.ParseTarget(u.Calendar)
			if err != nil {
				return err
			}
			var after struct {
				Spec any `json:"spec"`
			}
			if err := json.Unmarshal(result.After, &after); err != nil {
				return err
			}
			restored, err := calendar.SpecOf(after.Spec)
			if err != nil {
				return err
			}
			return calendar.Track(ctx, tx, restored, target, nil, nowSeconds())
		},
	}
}

func Undo(store *ledger.Store, env config.Env) []ledger.UndoDefinition {
	return []ledger.UndoDefinition{
		{Operation: "calendar_delete", Undoer: undoDelete(store, env)},
		{Operation: "calendar_recreate", Undoer: undoRecreate(store, env)},
	}
}

func RegisterLedgerUndoers(s *ledger.Store, env config.Env) error {
	for _, d := range Undo(s, env) {
		if err := s.RegisterUndo(d.Operation, d.Undoer); err != nil {
			return err
		}
	}
	return nil
}

func schema(properties string, required string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{%s},"required":%s,"additionalProperties":false}`, properties, required))
}

func Tools() []tool.Tool {
	return append(append(baseTools(), findSlotTool), updateTools...)
}

func Definitions() []module.ToolDefinition {
	tools := Tools()
	defs := make([]module.ToolDefinition, len(tools))
	for i, t := range tools {
		defs[i] = module.ToolDefinition{Tool: t}
	}
	return defs
}

func baseTools() []tool.Tool {
	return []tool.Tool{
		{
			Name: "calendar_list",
			Description: "List calendar events between start and end (end exclusive), merged from the iCloud calendar mirror and " +
				"the primary Google calendar, recurring events expanded. Optional calendar filters: 'icloud' for every " +
				"mirrored iCloud calendar, 'google' for Google, or a calendar name; empty lists both. Returns uid, calendar, " +
				"start, end, title, location.",
			InputSchema: schema(`"start":{"type":"string","description":"`+timeNote+`"},"end":{"type":"string","description":"`+timeNote+`"},"calendar":{"type":"string"}`, `["start","end"]`),
			Handler:     list,
			Effect:      tool.Read,
			Annotated:   true,
			Marks:       tool.Messages | tool.Untrusted,
			Contexts:    tool.Kinds{tool.Chat, tool.Cron, tool.Nightly, tool.Sub},
		},
		{
			Name: "calendar_create",
			Description: "Ask the operator to approve an event on their own iCloud or Google calendar; nothing is created until " +
				"they approve the card. Only for events the operator attends and time blocks. There is no attendees " +
				"parameter: invitations are never sent. Call only when the operator asked for this event in chat.",
			InputSchema: schema(eventProperties, `["calendar","title","start"]`),
			Handler:     propose,
			Effect:      tool.Propose,
			Annotated:   true,
			Channel:     tool.ChannelOwn,
			Contexts:    tool.Kinds{tool.Chat},
		},
		approval.Gate(tool.Tool{
			Name:        "calendar_delete",
			Description: "Ask the operator to approve deleting an event that atrium created earlier (by its uid); nothing is deleted until they approve the card. Other events cannot be deleted.",
			InputSchema: schema(`"uid":{"type":"string"}`, `["uid"]`),
			Effect:      tool.Propose, Undo: "calendar_recreate",
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  tool.Kinds{tool.Chat},
			Approval:  &tool.Approval{Apply: remove, Label: "Delete calendar event", Icon: "trash-2", Module: module.Calendar, Detail: "uid"},
		}),
		{
			Name: "event_propose",
			Description: "Ask the operator to approve a calendar event; nothing is created until they approve the card. Use for " +
				"events found in mail. No attendees. Pass source_message_id when the event comes from a mail.",
			InputSchema: schema(eventProperties+`,"source_message_id":{"type":"integer"}`, `["calendar","title","start"]`),
			Handler:     propose,
			Effect:      tool.Propose,
			Annotated:   true,
			Channel:     tool.ChannelOwn,
			Contexts:    tool.Kinds{tool.Chat, tool.Cron},
		},
	}
}

func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }
