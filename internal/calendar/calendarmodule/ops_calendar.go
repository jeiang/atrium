package calendarmodule

import (
	"github.com/jeiang/atrium/internal/application"
	calapp "github.com/jeiang/atrium/internal/calendar/application"
)

func (m Module) calendarOperations() ([]application.Definition, []application.Binding) {
	tool := func(target string, name application.Name) application.Binding {
		return application.Binding{Kind: application.Tool, Target: target, Uses: []application.Name{name}}
	}
	return append(calapp.Describe(), calapp.DescribePasses()...), append(m.calendarRoutes(nil), []application.Binding{
		{Kind: application.CLI, Target: "calendar sync", Uses: []application.Name{calapp.SyncName}},
		{Kind: application.CLI, Target: "calendar candidates", Uses: []application.Name{calapp.DraftCandidatesName}},
		{Kind: application.Job, Target: "calendar/caldav", Uses: []application.Name{calapp.SyncName}},
		{Kind: application.Job, Target: "calendar/calendar-candidates", Uses: []application.Name{calapp.DraftCandidatesName}},
		tool("calendar_list", calapp.ListName),
		tool("find_slot", calapp.FindSlotName),
		tool("calendar_create", calapp.ProposeCreateName),
		tool("event_propose", calapp.ProposeCreateName),
		tool("calendar_update", calapp.ProposeUpdateName),
		tool("calendar_rsvp", calapp.ProposeRSVPName),
		tool("calendar_delete", calapp.RequestDeleteName),
	}...)
}
