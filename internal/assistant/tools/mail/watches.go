package mailtools

import (
	"context"
	"encoding/json"
	"math"
	"strconv"

	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/tool"
)

var packagesSchema = json.RawMessage(`{"type":"object","properties":{"active_only":{"type":"boolean"}},"additionalProperties":false}`)

var birthdaysSchema = json.RawMessage(`{"type":"object","properties":{"days":{"type":"integer","minimum":0,"maximum":` +
	strconv.Itoa(mailapp.BirthdaysMaxDays) + `}},"additionalProperties":false}`)

func watchTools(svc func(*tool.Context) *mailapp.Service) []tool.Tool {
	return []tool.Tool{
		{
			Name: "packages",
			Description: "List packages and orders tracked from shipping and order emails (carrier, tracking number, merchant, " +
				"status). active_only (default true) hides delivered and stale ones.",
			InputSchema: packagesSchema,
			Handler: func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
				return packages(ctx, tc, args, svc)
			},
			Effect:    tool.Read,
			Annotated: true,
			Marks:     tool.PII | tool.Messages | tool.Untrusted,
			Contexts:  loopKinds,
		},
		{
			Name: "upcoming_birthdays",
			Description: "List birthdays and anniversaries from the address book within the next N days (default 14), soonest " +
				"first, with the age when the birth year is known.",
			InputSchema: birthdaysSchema,
			Handler: func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
				return upcomingBirthdays(ctx, tc, args, svc)
			},
			Effect:    tool.Read,
			Annotated: true,
			Marks:     tool.PII,
			Contexts:  loopKinds,
		},
	}
}

func packages(ctx context.Context, tc *tool.Context, args tool.Args, svc func(*tool.Context) *mailapp.Service) (any, error) {
	active, ok := args.Get("active_only")
	if !ok {
		active = true
	}
	activeOnly, isBool := active.(bool)
	if !isBool {
		return nil, tool.Error("active_only must be true or false")
	}
	read, err := svc(tc).Packages(ctx, mailapp.PackagesInput{ActiveOnly: activeOnly})
	if err != nil {
		return nil, err
	}
	items := read.Data.Packages
	out := make([]any, len(items))
	marks := tool.Marks(0)
	for i, p := range items {
		if p.TrackingNumber != "" || p.OrderRef != "" {
			marks |= tool.PII
		}
		if p.Merchant != "" || p.StatusText != "" || p.LastEvent != "" || p.LastEventDescription != "" {
			marks |= tool.Messages | tool.Untrusted
		}
		item := map[string]any{"id": p.ID, "carrier": p.Carrier, "tracking_number": p.TrackingNumber, "merchant": p.Merchant, "order_ref": p.OrderRef, "status": p.Status, "status_text": tool.Clip(p.StatusText, 200), "updated": p.Updated, "source_message_id": p.SourceMessageID}
		if p.TrackerID != "" {
			item["tracker_id"] = p.TrackerID
			item["aggregator"] = p.Aggregator
			item["last_sync"] = p.LastSync
			item["last_event"] = tool.Clip(p.LastEvent, 200)
			item["last_event_description"] = tool.Clip(p.LastEventDescription, 200)
		}
		out[i] = item
	}
	return tool.Marked{Value: map[string]any{"packages": out, "note": "Status comes from emails the operator received and, for tracked packages, from Ship24. Use track_package to register a package for live updates."}, Marks: marks}, nil
}

func upcomingBirthdays(ctx context.Context, tc *tool.Context, args tool.Args, svc func(*tool.Context) *mailapp.Service) (any, error) {
	var days *int64
	if _, given := args.Get("days"); given {
		n, err := tool.BoundedInt(args, "days", 0, math.MaxInt64, math.MinInt64)
		if err != nil {
			return nil, err
		}
		days = &n
	}
	loc, err := zone(ctx, tc)
	if err != nil {
		return nil, err
	}
	now := localTime(tc.Now(), loc)
	read, err := svc(tc).UpcomingBirthdays(ctx, mailapp.UpcomingBirthdaysInput{Today: mailapp.BirthdayDate(now.Year(), now.Month(), now.Day()), Days: days})
	if err != nil {
		return nil, mailboxFail(err)
	}
	found := read.Data.Upcoming
	out := make([]any, len(found))
	marks := tool.Marks(0)
	for i, item := range found {
		d := map[string]any{"name": item.Name, "kind": item.Kind, "label": item.Label, "date": item.Date, "days_until": int64(item.DaysUntil)}
		if item.Turning != nil {
			d["turning"] = int64(*item.Turning)
		}
		if item.Date != "" {
			marks |= tool.PII
		}
		out[i] = d
	}
	return tool.Marked{Value: map[string]any{"upcoming": out}, Marks: marks}, nil
}
