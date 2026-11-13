package calendartools

import (
	"context"

	calapp "github.com/jeiang/atrium/internal/calendar/application"
	"github.com/jeiang/atrium/internal/tool"
)

func proposeUpdate(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	in := calapp.UpdateInput{Calendar: target(args)}
	if err := texts(args, []string{"uid", "title", "start", "end", "location", "notes"},
		[]**string{&in.UID, &in.Title, &in.Start, &in.End, &in.Location, &in.Notes}); err != nil {
		return nil, err
	}
	flag, _ := args.Get("all_day")
	in.AllDay = truthy(flag)
	return proposed(service(tc).ProposeUpdate(ctx, in))
}

func proposeRSVP(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	var in calapp.RSVPInput
	if err := texts(args, []string{"calendar", "uid", "response", "title"}, []**string{&in.Calendar, &in.UID, &in.Response, &in.Title}); err != nil {
		return nil, err
	}
	return proposed(service(tc).ProposeRSVP(ctx, in))
}

var updateTools = []tool.Tool{
	{
		Name: "calendar_update",
		Description: "Propose a change to an existing event (by uid from calendar_list) for the operator to approve. The new title, " +
			"start, end, location and notes replace the old ones. Attendees are never added, removed or notified.",
		InputSchema: schema(`"uid":{"type":"string"},`+eventProperties, `["calendar","uid","title","start"]`),
		Handler:     proposeUpdate,
		Effect:      tool.Propose,
		Annotated:   true,
		Channel:     tool.ChannelOwn,
		Contexts:    tool.Kinds{tool.Chat, tool.Cron},
	},
	{
		Name: "calendar_rsvp",
		Description: "Propose replying to an invitation (by uid from calendar_list) with accepted, declined or tentative, for the " +
			"operator to approve. Only the operator's own response changes.",
		InputSchema: schema(`"calendar":{"type":"string","enum":["icloud","google"]},"uid":{"type":"string"},`+
			`"response":{"type":"string","enum":["accepted","declined","tentative"]},"title":{"type":"string"}`, `["calendar","uid","response"]`),
		Handler:   proposeRSVP,
		Effect:    tool.Propose,
		Annotated: true,
		Channel:   tool.ChannelPeople,
		Contexts:  tool.Kinds{tool.Chat, tool.Cron},
	},
}
