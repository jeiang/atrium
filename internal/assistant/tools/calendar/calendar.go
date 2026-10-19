// Package calendartools is agent/tools/calendar.py: calendar_list, calendar_create, calendar_delete and
// event_propose, and the undoers of the two direct writes.
package calendartools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

const timeNote = "ISO-8601 (2026-10-16T09:00 or 2026-10-16); times without an offset are in the operator's timezone."

var eventProperties = `"calendar":{"type":"string","enum":["icloud","google"]},"title":{"type":"string"},` +
	`"start":{"type":"string","description":"` + timeNote + `"},` +
	`"end":{"type":"string","description":"Exclusive end, same format. Default: one hour (all-day: one day) after start."},` +
	`"all_day":{"type":"boolean","description":"start/end are dates"},"location":{"type":"string"},"notes":{"type":"string"}`

// wrap is _wrap: a missing provider is NotConfigured, a CalendarError a ToolError; any other failure is the
// Python exception that raised it.
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
	if class := py.ExceptionClass(err); class != "RuntimeError" {
		return &tool.Raised{Class: class}
	}
	return err
}

func optionalText(args py.Dict, name string) (any, error) {
	text, ok, err := tool.TextArg(args, name, false)
	if err != nil || !ok {
		return nil, err
	}
	return text, nil
}

func requiredText(args py.Dict, name string) (any, error) {
	text, ok, err := tool.TextArg(args, name, true)
	if err != nil || !ok {
		return nil, err
	}
	return text, nil
}

func spec(args py.Dict, tz *time.Location) (string, calendar.Spec, error) {
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
	raw = raw.Set("title", title).Set("start", start).Set("all_day", py.Truthy(flag))
	chosen, _ := args.Get("calendar")
	target, _ := chosen.(string)
	if target != calendar.ICloud && target != calendar.Google {
		return "", calendar.Spec{}, tool.Error("calendar must be one of " + calendar.TargetsText)
	}
	normalized, err := calendar.NormalizeSpec(raw, tz, "")
	return target, normalized, wrap(err)
}

func list(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
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
	listing, err := calendar.ListEvents(ctx, tc.Env, start, end, target)
	if err != nil {
		return nil, wrap(err)
	}
	return listing.Dict(), nil
}

func create(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	tz, err := calendar.Zone(tc.Env)
	if err != nil {
		return nil, err
	}
	target, normalized, err := spec(args, tz)
	if err != nil {
		return nil, err
	}
	ref, err := calendar.CreateEvent(ctx, tc.Env, target, normalized, tz)
	if err != nil {
		return nil, wrap(err)
	}
	err = tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		if err := calendar.Track(ctx, tx, normalized, target, nil, tc.Now()); err != nil {
			return err
		}
		_, err := writes.Record(ctx, tx, writes.New{
			TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "calendar_create",
			Target: target + ":" + normalized.UID,
			After:  py.Dict{{Key: "calendar", Value: target}, {Key: "spec", Value: normalized.Dict()}},
			Undo:   py.Dict{{Key: "op", Value: "calendar_delete"}, {Key: "calendar", Value: target}, {Key: "uid", Value: normalized.UID}},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return append(ref.Dict(), py.Pair{Key: "title", Value: normalized.Title}, py.Pair{Key: "start", Value: normalized.Start},
		py.Pair{Key: "end", Value: normalized.End}), nil
}

func remove(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
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
	stored, err := py.JSONLoads([]byte(row.Spec))
	if err != nil {
		return nil, err
	}
	if err := calendar.DeleteEvent(ctx, tc.Env, row.Calendar, uid); err != nil {
		return nil, wrap(err)
	}
	err = tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		if err := calendar.Untrack(ctx, tx, uid, tc.Now()); err != nil {
			return err
		}
		_, err := writes.Record(ctx, tx, writes.New{
			TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "calendar_delete",
			Target: row.Calendar + ":" + uid,
			Before: py.Dict{{Key: "calendar", Value: row.Calendar}, {Key: "spec", Value: stored}},
			Undo:   py.Dict{{Key: "op", Value: "calendar_recreate"}, {Key: "calendar", Value: row.Calendar}, {Key: "spec", Value: stored}},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return py.Dict{{Key: "deleted", Value: uid}, {Key: "calendar", Value: row.Calendar}}, nil
}

func propose(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
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
	raw = raw.Set("all_day", py.Truthy(flag)).Set("calendar", target)
	source := py.Dict{}
	messageID, hasID := args.Get("source_message_id")
	switch id := messageID.(type) {
	case int64, bool, *big.Int:
		source = source.Set("message_id", id)
	default:
		hasID = false
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
		return py.Dict{{Key: "proposed", Value: false}, {Key: "reason", Value: "an identical proposal already exists"}}, nil
	}
	if hasID && py.Truthy(messageID) {
		if err := markDrafted(ctx, tc.DB, messageID, tc.Now()); err != nil {
			return nil, err
		}
	}
	return py.Dict{{Key: "proposed", Value: true}, {Key: "proposal_id", Value: id}}, nil
}

func markDrafted(ctx context.Context, d *db.DB, messageID any, now float64) error {
	id := messageID
	if b, ok := id.(bool); ok {
		id = int64(0)
		if b {
			id = int64(1)
		}
	}
	if n, ok := id.(*big.Int); ok {
		id = n.String()
	}
	res, err := d.Write.ExecContext(ctx, "UPDATE event_candidates SET status = ?, ts = ? WHERE message_id = ?", "drafted", now, id)
	if err != nil {
		return err
	}
	if changed, err := res.RowsAffected(); err != nil || changed > 0 {
		return err
	}
	_, err = d.Write.ExecContext(ctx, "INSERT OR REPLACE INTO event_candidates (message_id, reason, status, ts) VALUES (?, ?, ?, ?)",
		id, "", "drafted", now)
	return err
}

func undoDelete(ctx context.Context, q db.Querier, info map[string]any) (any, error) {
	target, _ := info["calendar"].(string)
	uid, _ := info["uid"].(string)
	if err := calendar.DeleteEvent(ctx, config.OS, target, uid); err != nil {
		var failed calendar.Error
		if errors.As(err, &failed) {
			return nil, writes.UndoError(failed.Error())
		}
		return nil, err
	}
	if err := calendar.Untrack(ctx, q, uid, nowSeconds()); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "deleted", Value: uid}}, nil
}

func undoRecreate(ctx context.Context, q db.Querier, info map[string]any) (any, error) {
	target, _ := info["calendar"].(string)
	restored, err := calendar.SpecOf(info["spec"])
	if err != nil {
		return nil, err
	}
	restored.UID = calendar.NewUID()
	tz, err := calendar.Zone(config.OS)
	if err != nil {
		return nil, err
	}
	if _, err := calendar.CreateEvent(ctx, config.OS, target, restored, tz); err != nil {
		var failed calendar.Error
		if errors.As(err, &failed) {
			return nil, writes.UndoError(failed.Error())
		}
		return nil, err
	}
	if err := calendar.Track(ctx, q, restored, target, nil, nowSeconds()); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "created", Value: restored.UID}}, nil
}

// RegisterUndoers adds the undoers of calendar_create and calendar_delete.
func RegisterUndoers(r *writes.Registry) {
	r.Register("calendar_delete", undoDelete)
	r.Register("calendar_recreate", undoRecreate)
}

func schema(properties string, required string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{%s},"required":%s,"additionalProperties":false}`, properties, required))
}

// Tools is TOOLS of calendar.py.
func Tools() []tool.Tool {
	return []tool.Tool{
		{
			Name: "calendar_list",
			Description: "List calendar events between start and end (end exclusive), merged from the iCloud calendar mirror and " +
				"the primary Google calendar, recurring events expanded. Optional calendar filters by calendar name " +
				"or 'google'. Returns uid, calendar, start, end, title, location.",
			InputSchema: schema(`"start":{"type":"string","description":"`+timeNote+`"},"end":{"type":"string","description":"`+timeNote+`"},"calendar":{"type":"string"}`, `["start","end"]`),
			Handler:     list,
			Effect:      tool.Read,
			Contexts:    tool.Kinds{tool.Chat, tool.Cron, tool.Nightly, tool.Sub},
		},
		{
			Name: "calendar_create",
			Description: "Create an event on the operator's own iCloud or Google calendar. Only for events the operator attends " +
				"and time blocks. There is no attendees parameter: invitations are never sent. Call only when the " +
				"operator asked for this event in chat.",
			InputSchema: schema(eventProperties, `["calendar","title","start"]`),
			Handler:     create,
			Effect:      tool.Write,
			Contexts:    tool.Kinds{tool.Chat},
		},
		{
			Name:        "calendar_delete",
			Description: "Delete an event that atrium created earlier (by its uid from calendar_create). Other events cannot be deleted.",
			InputSchema: schema(`"uid":{"type":"string"}`, `["uid"]`),
			Handler:     remove,
			Effect:      tool.Write,
			Contexts:    tool.Kinds{tool.Chat},
		},
		{
			Name: "event_propose",
			Description: "Propose a calendar event for the operator to approve on the Proposals page (nothing is created until " +
				"they approve). Use for events found in mail. No attendees. Pass source_message_id when the event comes " +
				"from a mail.",
			InputSchema: schema(eventProperties+`,"source_message_id":{"type":"integer"}`, `["calendar","title","start"]`),
			Handler:     propose,
			Effect:      tool.Propose,
			Contexts:    tool.Kinds{tool.Chat, tool.Cron},
		},
	}
}

func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }
