package calendartools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant/approval"
	calapp "github.com/jeiang/atrium/internal/calendar/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/tool"
)

const timeNote = "ISO-8601 (2026-10-16T09:00 or 2026-10-16); times without an offset are in the operator's timezone."

var eventProperties = `"calendar":{"type":"string","enum":["icloud","google"]},"title":{"type":"string"},` +
	`"start":{"type":"string","description":"` + timeNote + `"},` +
	`"end":{"type":"string","description":"Exclusive end, same format. Default: one hour (all-day: one day) after start."},` +
	`"all_day":{"type":"boolean","description":"start/end are dates"},"location":{"type":"string"},"notes":{"type":"string"}`

func service(tc *tool.Context) *calapp.Service {
	return calapp.New(tc.DB, tc.Env, tc.Ledger, tc.Now)
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

func optionalText(args tool.Args, name string) (*string, error) {
	text, ok, err := tool.TextArg(args, name, false)
	if err != nil || !ok {
		return nil, err
	}
	return &text, nil
}

func requiredText(args tool.Args, name string) (string, error) {
	text, _, err := tool.TextArg(args, name, true)
	return text, err
}

func target(args tool.Args) string {
	v, _ := args.Get("calendar")
	s, _ := v.(string)
	return s
}

func listed(read calapp.Listing) tool.Marked {
	var marks tool.Marks
	if read.Messages {
		marks |= tool.Messages
	}
	if read.Untrusted {
		marks |= tool.Untrusted
	}
	return tool.Marked{Value: read.Value, Marks: marks}
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
	filter, _, err := tool.TextArg(args, "calendar", false)
	if err != nil {
		return nil, err
	}
	read, err := service(tc).List(ctx, calapp.ListInput{Start: start, End: end, Calendar: filter})
	if err != nil {
		return nil, tool.Fail(err)
	}
	return listed(read.Data), nil
}

func remove(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	uid, _, err := tool.TextArg(args, "uid", true)
	if err != nil {
		return nil, err
	}
	change, err := service(tc).RequestDelete(ctx, calapp.DeleteInput{UID: uid, ThreadID: tc.ThreadID, RunID: tc.RunID, ProposalID: tc.ProposalID})
	if err != nil {
		return nil, tool.Fail(err)
	}
	return map[string]any{"deleted": change.Data.Deleted, "calendar": change.Data.Calendar}, nil
}

func proposed(change application.Change[calapp.Proposed], err error) (any, error) {
	if err != nil {
		return nil, tool.Fail(err)
	}
	if !change.Data.Proposed {
		return map[string]any{"proposed": false, "reason": change.Data.Reason}, nil
	}
	return map[string]any{"proposed": true, "proposal_id": change.Data.ProposalID}, nil
}

func eventInput(args tool.Args) (calapp.EventInput, error) {
	in := calapp.EventInput{Calendar: target(args)}
	err := texts(args, []string{"title", "start", "end", "location", "notes"}, []**string{&in.Title, &in.Start, &in.End, &in.Location, &in.Notes})
	flag, _ := args.Get("all_day")
	in.AllDay = truthy(flag)
	return in, err
}

func texts(args tool.Args, names []string, fields []**string) error {
	for i, name := range names {
		v, err := optionalText(args, name)
		if err != nil {
			return err
		}
		*fields[i] = v
	}
	return nil
}

func propose(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	in, err := eventInput(args)
	if err != nil {
		return nil, err
	}
	if _, hasID := args.Get("source_message_id"); hasID {
		messageID, _, err := tool.IntArg(args, "source_message_id", true)
		if err != nil {
			return nil, err
		}
		in.SourceMessageID = &messageID
	}
	return proposed(service(tc).ProposeCreate(ctx, in))
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
