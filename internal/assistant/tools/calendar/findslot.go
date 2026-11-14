package calendartools

import (
	"context"

	calapp "github.com/jeiang/atrium/internal/calendar/application"
	"github.com/jeiang/atrium/internal/tool"
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
	filter, _, err := tool.TextArg(args, "calendar", false)
	if err != nil {
		return nil, err
	}
	read, err := service(tc).FindSlot(ctx, calapp.FindSlotInput{Start: start, End: end, Minutes: minutes, Calendar: filter})
	if err != nil {
		return nil, tool.Fail(err)
	}
	var marks tool.Marks
	if read.Data.Messages {
		marks = tool.Messages
	}
	return tool.Marked{Value: read.Data.Value, Marks: marks}, nil
}

var durationBound = tool.NewCeiling(30, 5, 1440)

var findSlotTool = tool.Tool{
	Name: "find_slot",
	Description: "Find free time of at least duration_minutes between start and end, inside the operator's working hours and days " +
		"(settings work_hours and work_days), against the iCloud mirror and Google calendar. Timed events block time; " +
		"all-day events do not. Optional calendar filters like calendar_list. Read-only.",
	InputSchema: schema(`"start":{"type":"string","description":"`+timeNote+`"},"end":{"type":"string","description":"`+timeNote+`"},`+
		`"duration_minutes":`+durationBound.Schema()+`,"calendar":{"type":"string"}`, `["start","end"]`),
	Handler:   findSlot,
	Effect:    tool.Read,
	Annotated: true,
	Marks:     tool.Messages,
	Contexts:  tool.Kinds{tool.Chat, tool.Cron, tool.Nightly, tool.Sub},
}
