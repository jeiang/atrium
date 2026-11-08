package calendartools

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/py"
)

func findSlot(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	start, err := requiredText(args, "start")
	if err != nil {
		return nil, err
	}
	end, err := requiredText(args, "end")
	if err != nil {
		return nil, err
	}
	minutes, err := durationBound.Clamp(ctx, tc.DB.Read, args, "duration_minutes")
	if err != nil {
		return nil, err
	}
	target, _, err := tool.TextArg(args, "calendar", false)
	if err != nil {
		return nil, err
	}
	set, err := calendar.LoadSettings(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	found, err := calendar.FindSlots(ctx, tc.DB.Read, tc.Env, set, start, end, time.Duration(minutes)*time.Minute, target)
	if err != nil {
		return nil, wrap(err)
	}
	windows := make([]any, len(found.Windows))
	for i, w := range found.Windows {
		windows[i] = py.Dict{{Key: "start", Value: w.Start.Format(time.RFC3339)}, {Key: "end", Value: w.End.Format(time.RFC3339)}}
	}
	notes := make([]any, len(found.Notes))
	for i, n := range found.Notes {
		notes[i] = n
	}
	return py.Dict{{Key: "windows", Value: windows}, {Key: "notes", Value: notes}}, nil
}

var durationBound = tool.NewCeiling(30, 5, 1440)

var findSlotTool = tool.Tool{
	Name: "find_slot",
	Description: "Find free time of at least duration_minutes between start and end, inside the operator's working hours and days " +
		"(settings work_hours and work_days), against the iCloud mirror and Google calendar. Timed events block time; " +
		"all-day events do not. Read-only.",
	InputSchema: schema(`"start":{"type":"string","description":"`+timeNote+`"},"end":{"type":"string","description":"`+timeNote+`"},`+
		`"duration_minutes":`+durationBound.Schema()+`,"calendar":{"type":"string"}`, `["start","end"]`),
	Handler:  findSlot,
	Effect:   tool.Read,
	Contexts: tool.Kinds{tool.Chat, tool.Cron, tool.Nightly, tool.Sub},
}
