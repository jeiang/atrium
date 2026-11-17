package mailtools

import (
	"context"
	"encoding/json"

	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/tool"
)

var packagesSchema = json.RawMessage(`{"type":"object","properties":{"active_only":{"type":"boolean"}},"additionalProperties":false}`)

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
