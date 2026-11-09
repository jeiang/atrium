package calendartools

import (
	"context"

	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/tool"
)

func proposed(id int64, created bool, err error) (any, error) {
	if err != nil {
		if converted := tool.Domain[calendar.ProposalError, tool.Error](err); converted != err {
			return nil, converted
		}
		return nil, wrap(err)
	}
	if !created {
		return map[string]any{"proposed": false, "reason": "an identical proposal already exists"}, nil
	}
	return map[string]any{"proposed": true, "proposal_id": id}, nil
}

func proposeUpdate(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	raw := map[string]any{}
	for _, name := range []string{"uid", "title", "start", "end", "location", "notes"} {
		v, err := optionalText(args, name)
		if err != nil {
			return nil, err
		}
		raw[name] = v
	}
	flag, _ := args.Get("all_day")
	target, _ := args.Get("calendar")
	raw["all_day"], raw["calendar"] = truthy(flag), target
	return proposed((&calendar.Proposals{DB: tc.DB, Now: tc.Now, Env: tc.Env}).ProposeUpdate(ctx, raw))
}

func proposeRSVP(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	raw := map[string]any{}
	for _, name := range []string{"calendar", "uid", "response", "title"} {
		v, err := optionalText(args, name)
		if err != nil {
			return nil, err
		}
		raw[name] = v
	}
	return proposed((&calendar.Proposals{DB: tc.DB, Now: tc.Now, Env: tc.Env}).ProposeRSVP(ctx, raw))
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
