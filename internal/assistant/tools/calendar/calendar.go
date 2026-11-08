package calendartools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/calendar/candidates"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

const timeNote = "ISO-8601 (2026-10-16T09:00 or 2026-10-16); times without an offset are in the operator's timezone."

var eventProperties = `"calendar":{"type":"string","enum":["icloud","google"]},"title":{"type":"string"},` +
	`"start":{"type":"string","description":"` + timeNote + `"},` +
	`"end":{"type":"string","description":"Exclusive end, same format. Default: one hour (all-day: one day) after start."},` +
	`"all_day":{"type":"boolean","description":"start/end are dates"},"location":{"type":"string"},"notes":{"type":"string"}`

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
	case py.Dict:
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

func spec(args tool.Args, tz *time.Location, defaultMinutes int) (calendar.Target, calendar.Spec, error) {
	raw := py.Dict{}
	for _, name := range []string{"end", "location", "notes"} {
		v, err := optionalText(args, name)
		if err != nil {
			return "", calendar.Spec{}, err
		}
		raw = raw.Set(name, v)
	}
	title, err := requiredText(args, "title")
	if err != nil {
		return "", calendar.Spec{}, err
	}
	start, err := requiredText(args, "start")
	if err != nil {
		return "", calendar.Spec{}, err
	}
	flag, _ := args.Get("all_day")
	raw = raw.Set("title", title).Set("start", start).Set("all_day", truthy(flag))
	chosen, _ := args.Get("calendar")
	text, _ := chosen.(string)
	target, err := calendar.ParseTarget(text)
	if err != nil {
		return "", calendar.Spec{}, tool.Error(err.Error())
	}
	normalized, err := calendar.NormalizeSpec(raw, tz, "", defaultMinutes)
	return target, normalized, wrap(err)
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
	return listing.Dict(), nil
}

func create(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	tz, err := calendar.Zone(tc.Env)
	if err != nil {
		return nil, err
	}
	set, err := calendar.LoadSettings(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	target, normalized, err := spec(args, tz, set.DefaultMinutes)
	if err != nil {
		return nil, err
	}
	intent, err := writes.Begin(ctx, tc.DB.Write, writes.New{
		TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "calendar_create",
		Target: string(target) + ":" + normalized.UID,
	})
	if err != nil {
		return nil, err
	}
	ref, err := calendar.CreateEvent(ctx, tc.Env, set, target, normalized, tz)
	if err != nil {
		if calendar.OutcomeUnknown(err) {
			return nil, wrap(err)
		}
		_ = writes.Discard(ctx, tc.DB.Write, intent)
		return nil, wrap(err)
	}
	err = tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		if err := calendar.Track(ctx, tx, normalized, target, nil, tc.Now()); err != nil {
			return err
		}
		return writes.Settle(ctx, tx, tc.Undoers, intent, writes.New{
			Target: string(target) + ":" + normalized.UID,
			After:  map[string]any{"calendar": string(target), "spec": normalized.Dict()},
			Undo:   deleteUndo{Op: "calendar_delete", Calendar: string(target), UID: normalized.UID},
		})
	})
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, p := range ref.Dict() {
		out[p.Key] = p.Value
	}
	normalizedDict := normalized.Dict()
	out["title"] = normalized.Title
	out["start"], _ = normalizedDict.Get("start")
	out["end"], _ = normalizedDict.Get("end")
	return out, nil
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
	intent, err := writes.Begin(ctx, tc.DB.Write, writes.New{
		TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "calendar_delete",
		Target: row.Calendar + ":" + uid,
	})
	if err != nil {
		return nil, err
	}
	if err := calendar.DeleteEvent(ctx, tc.Env, set, rowTarget, uid); err != nil {
		if calendar.OutcomeUnknown(err) {
			return nil, wrap(err)
		}
		_ = writes.Discard(ctx, tc.DB.Write, intent)
		return nil, wrap(err)
	}
	err = tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		if err := calendar.Untrack(ctx, tx, uid, tc.Now()); err != nil {
			return err
		}
		return writes.Settle(ctx, tx, tc.Undoers, intent, writes.New{
			Target: row.Calendar + ":" + uid,
			Before: map[string]any{"calendar": row.Calendar, "spec": stored},
			Undo:   recreateUndo{Op: "calendar_recreate", Calendar: row.Calendar, Spec: stored},
		})
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"deleted": uid, "calendar": row.Calendar}, nil
}

func propose(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	raw := py.Dict{}
	for _, name := range []string{"title", "start", "end", "location", "notes"} {
		v, err := optionalText(args, name)
		if err != nil {
			return nil, err
		}
		raw = raw.Set(name, v)
	}
	flag, _ := args.Get("all_day")
	target, _ := args.Get("calendar")
	raw = raw.Set("all_day", truthy(flag)).Set("calendar", target)
	source := py.Dict{}
	if _, hasID := args.Get("source_message_id"); hasID {
		messageID, _, err := tool.IntArg(args, "source_message_id", true)
		if err != nil {
			return nil, err
		}
		source = source.Set("message_id", messageID)
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
	if stored, ok := source.Get("message_id"); ok {
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

func undoDelete(env config.Env) writes.Undoer {
	return func(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
		var u deleteUndo
		if err := json.Unmarshal(raw, &u); err != nil {
			return nil, writes.UndoError("calendar undo payload is invalid")
		}
		target, err := calendar.ParseTarget(u.Calendar)
		if err != nil {
			return nil, writes.UndoError(err.Error())
		}
		uid := u.UID
		set, err := calendar.LoadSettings(ctx, q)
		if err != nil {
			return nil, err
		}
		if err := calendar.DeleteEvent(ctx, env, set, target, uid); err != nil {
			var failed calendar.Error
			if errors.As(err, &failed) {
				return nil, writes.UndoError(failed.Error())
			}
			return nil, err
		}
		if err := calendar.Untrack(ctx, q, uid, nowSeconds()); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": uid}, nil
	}
}

func undoRecreate(env config.Env) writes.Undoer {
	return func(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
		var u recreateUndo
		if err := json.Unmarshal(raw, &u); err != nil {
			return nil, writes.UndoError("calendar undo payload is invalid")
		}
		target, err := calendar.ParseTarget(u.Calendar)
		if err != nil {
			return nil, writes.UndoError(err.Error())
		}
		restored, err := calendar.SpecOf(u.Spec)
		if err != nil {
			return nil, err
		}
		restored.UID = calendar.NewUID()
		tz, err := calendar.Zone(env)
		if err != nil {
			return nil, err
		}
		set, err := calendar.LoadSettings(ctx, q)
		if err != nil {
			return nil, err
		}
		if _, err := calendar.CreateEvent(ctx, env, set, target, restored, tz); err != nil {
			var failed calendar.Error
			if errors.As(err, &failed) {
				return nil, writes.UndoError(failed.Error())
			}
			return nil, err
		}
		if err := calendar.Track(ctx, q, restored, target, nil, nowSeconds()); err != nil {
			return nil, err
		}
		return map[string]any{"created": restored.UID}, nil
	}
}

func RegisterUndoers(r *writes.Registry, env config.Env) {
	r.Register("calendar_delete", undoDelete(env))
	r.Register("calendar_recreate", undoRecreate(env))
}

func schema(properties string, required string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{%s},"required":%s,"additionalProperties":false}`, properties, required))
}

func Tools() []tool.Tool {
	return append(append(baseTools(), findSlotTool), updateTools...)
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
			Flags:       tool.Flags{PrivateData: true},
			Contexts:    tool.Kinds{tool.Chat, tool.Cron, tool.Nightly, tool.Sub},
		},
		{
			Name: "calendar_create",
			Description: "Create an event on the operator's own iCloud or Google calendar. Only for events the operator attends " +
				"and time blocks. There is no attendees parameter: invitations are never sent. Call only when the " +
				"operator asked for this event in chat.",
			InputSchema: schema(eventProperties, `["calendar","title","start"]`),
			Handler:     create,
			Effect:      tool.WriteUndo, Undo: "calendar_delete",
			Annotated: true,
			Flags:     tool.Flags{ExternalEffect: true},
			Contexts:  tool.Kinds{tool.Chat},
		},
		{
			Name:        "calendar_delete",
			Description: "Delete an event that atrium created earlier (by its uid from calendar_create). Other events cannot be deleted.",
			InputSchema: schema(`"uid":{"type":"string"}`, `["uid"]`),
			Handler:     remove,
			Effect:      tool.WriteUndo, Undo: "calendar_recreate",
			Annotated: true,
			Flags:     tool.Flags{ExternalEffect: true},
			Contexts:  tool.Kinds{tool.Chat},
		},
		{
			Name: "event_propose",
			Description: "Propose a calendar event for the operator to approve on the Proposals page (nothing is created until " +
				"they approve). Use for events found in mail. No attendees. Pass source_message_id when the event comes " +
				"from a mail.",
			InputSchema: schema(eventProperties+`,"source_message_id":{"type":"integer"}`, `["calendar","title","start"]`),
			Handler:     propose,
			Effect:      tool.Propose,
			Annotated:   true,
			Contexts:    tool.Kinds{tool.Chat, tool.Cron},
		},
	}
}

func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }
