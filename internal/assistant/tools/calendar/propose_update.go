package calendartools

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/py"
)

func proposed(id int64, created bool, err error) (any, error) {
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
	return py.Dict{{Key: "proposed", Value: true}, {Key: "proposal_id", Value: id}}, nil
}

func proposeUpdate(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	raw := py.Dict{}
	for _, name := range []string{"uid", "title", "start", "end", "location", "notes"} {
		v, err := optionalText(args, name)
		if err != nil {
			return nil, err
		}
		raw = raw.Set(name, v)
	}
	flag, _ := args.Get("all_day")
	target, _ := args.Get("calendar")
	raw = raw.Set("all_day", py.Truthy(flag)).Set("calendar", target)
	return proposed((&calendar.Proposals{DB: tc.DB, Now: tc.Now, Env: tc.Env}).ProposeUpdate(ctx, raw))
}

func proposeRSVP(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	raw := py.Dict{}
	for _, name := range []string{"calendar", "uid", "response", "title"} {
		v, err := optionalText(args, name)
		if err != nil {
			return nil, err
		}
		raw = raw.Set(name, v)
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
		Contexts:    tool.Kinds{tool.Chat, tool.Cron},
	},
	{
		Name: "calendar_rsvp",
		Description: "Propose replying to an invitation (by uid from calendar_list) with accepted, declined or tentative, for the " +
			"operator to approve. Only the operator's own response changes.",
		InputSchema: schema(`"calendar":{"type":"string","enum":["icloud","google"]},"uid":{"type":"string"},`+
			`"response":{"type":"string","enum":["accepted","declined","tentative"]},"title":{"type":"string"}`, `["calendar","uid","response"]`),
		Handler:  proposeRSVP,
		Effect:   tool.Propose,
		Contexts: tool.Kinds{tool.Chat, tool.Cron},
	},
}
