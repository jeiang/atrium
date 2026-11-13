package calendarmodule

import (
	"github.com/jeiang/atrium/internal/application"
	calapp "github.com/jeiang/atrium/internal/calendar/application"
)

func calendarOperations() ([]application.Definition, []application.Binding) {
	tool := func(target string, name application.Name) application.Binding {
		return application.Binding{Kind: application.Tool, Target: target, Uses: []application.Name{name}}
	}
	return calapp.Describe(), []application.Binding{
		tool("calendar_list", calapp.ListName),
		tool("find_slot", calapp.FindSlotName),
		tool("calendar_create", calapp.ProposeCreateName),
		tool("event_propose", calapp.ProposeCreateName),
		tool("calendar_update", calapp.ProposeUpdateName),
		tool("calendar_rsvp", calapp.ProposeRSVPName),
		tool("calendar_delete", calapp.RequestDeleteName),
	}
}
