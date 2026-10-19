package mailtools

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail/birthdays"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/py"
)

var packagesSchema = json.RawMessage(`{"type":"object","properties":{"active_only":{"type":"boolean"}},"additionalProperties":false}`)

var birthdaysSchema = json.RawMessage(`{"type":"object","properties":{"days":{"type":"integer","minimum":0,"maximum":` +
	strconv.Itoa(birthdays.MaxDays) + `}},"additionalProperties":false}`)

func watchTools() []tool.Tool {
	return []tool.Tool{
		{
			Name: "packages",
			Description: "List packages and orders tracked from shipping and order emails (carrier, tracking number, merchant, " +
				"status). active_only (default true) hides delivered and stale ones.",
			InputSchema: packagesSchema,
			Handler:     packages,
			Effect:      tool.Read,
			Contexts:    loopKinds,
		},
		{
			Name: "upcoming_birthdays",
			Description: "List birthdays and anniversaries from the address book within the next N days (default 14), soonest " +
				"first, with the age when the birth year is known.",
			InputSchema: birthdaysSchema,
			Handler:     upcomingBirthdays,
			Effect:      tool.Read,
			Contexts:    loopKinds,
		},
	}
}

func packages(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	active, ok := args.Get("active_only")
	if !ok {
		active = true
	}
	activeOnly, isBool := active.(bool)
	if !isBool {
		return nil, tool.Error("active_only must be true or false")
	}
	items, err := watches.ListPackages(ctx, tc.DB.Read, tc.Env, tc.Now(), activeOnly, false)
	if errors.Is(err, httpx.ErrKey) {
		return nil, &tool.Raised{Class: "ZoneInfoNotFoundError"}
	}
	if err != nil {
		return nil, err
	}
	out := make([]any, len(items))
	for i, p := range items {
		out[i] = py.Dict{
			{Key: "carrier", Value: p.Carrier},
			{Key: "tracking_number", Value: p.TrackingNumber},
			{Key: "merchant", Value: p.Merchant},
			{Key: "order_ref", Value: p.OrderRef},
			{Key: "status", Value: p.Status},
			{Key: "status_text", Value: tool.Clip(p.StatusText, 200)},
			{Key: "updated", Value: p.Updated},
			{Key: "source_message_id", Value: p.SourceMessageID},
		}
	}
	return py.Dict{
		{Key: "packages", Value: out},
		{Key: "note", Value: "Status comes only from emails the operator received; there is no carrier lookup, so it can lag."},
	}, nil
}

func upcomingBirthdays(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	days, err := tool.BoundedInt(args, "days", birthdays.DefaultDays, birthdays.MaxDays, 0)
	if err != nil {
		return nil, err
	}
	loc, err := zone(tc.Env)
	if err != nil {
		return nil, err
	}
	now := localTime(tc.Now(), loc)
	today := birthdays.Date(now.Year(), now.Month(), now.Day())
	found, ok, err := birthdays.UpcomingFor(ctx, tc.DB.Read, tc.Cfg, today, int(days))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, tool.NotConfigured("ATRIUM_CONTACTS_DIR is not set")
	}
	out := make([]any, len(found))
	for i, item := range found {
		d := py.Dict{
			{Key: "name", Value: item.Name},
			{Key: "kind", Value: item.Kind},
			{Key: "label", Value: item.Label},
			{Key: "date", Value: item.Date},
			{Key: "days_until", Value: int64(item.DaysUntil)},
		}
		if item.Turning != nil {
			d = append(d, py.Pair{Key: "turning", Value: int64(*item.Turning)})
		}
		out[i] = d
	}
	return py.Dict{{Key: "upcoming", Value: out}}, nil
}
