package assistantmodule

import (
	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/tool"
)

func scheduleToolService(tc *tool.Context) *asstapp.Service {
	return asstapp.New(tc.DB, tc.Env, tc.Now).WithLedger(tc.Ledger)
}

func scheduleOperations() ([]application.Definition, []application.Binding) {
	uses := func(names ...application.Name) []application.Name { return names }
	return []application.Definition{
			asstapp.DescribeScheduleCreate(), asstapp.DescribeScheduleUpdate(), asstapp.DescribeScheduleEnabled(),
			asstapp.DescribeScheduleCancel(), asstapp.DescribeScheduleList(), asstapp.DescribeScheduleHistory(),
		}, []application.Binding{
			{Kind: application.HTML, Target: "GET /scheduled", Uses: uses(asstapp.ScheduleListName, asstapp.ScheduleHistoryName)},
			{Kind: application.HTML, Target: "POST /scheduled/{job_id}/toggle", Uses: uses(asstapp.ScheduleEnabledName, asstapp.ScheduleListName, asstapp.ScheduleHistoryName)},
			{Kind: application.HTML, Target: "POST /scheduled/{job_id}/cancel", Uses: uses(asstapp.ScheduleCancelName, asstapp.ScheduleListName, asstapp.ScheduleHistoryName)},
			{Kind: application.HTML, Target: "POST /scheduled/{job_id}/schedule", Uses: uses(asstapp.ScheduleUpdateName, asstapp.ScheduleListName, asstapp.ScheduleHistoryName)},
			{Kind: application.Tool, Target: "schedule_create", Uses: uses(asstapp.ScheduleCreateName)},
			{Kind: application.Tool, Target: "schedule_update", Uses: uses(asstapp.ScheduleUpdateName)},
			{Kind: application.Tool, Target: "schedule_cancel", Uses: uses(asstapp.ScheduleCancelName)},
			{Kind: application.Tool, Target: "schedule_list", Uses: uses(asstapp.ScheduleListName)},
		}
}
