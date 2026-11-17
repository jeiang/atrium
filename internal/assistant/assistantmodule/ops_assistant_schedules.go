package assistantmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/schedulecmd"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/tool"
)

func scheduleToolService(tc *tool.Context) *asstapp.Service {
	return asstapp.New(tc.DB, tc.Env, tc.Now).WithLedger(tc.Ledger)
}

func scheduleCLIService(env config.Env, now func() float64) schedulecmd.Open {
	open := memoryCLIService(env, now)
	return func(ctx context.Context) (*asstapp.Service, func(), error) {
		svc, done, err := open(ctx)
		if err != nil {
			return nil, nil, err
		}
		if set := module.SetFrom(ctx); set != nil {
			svc = svc.WithJobs(set.Catalog().Jobs())
		}
		return svc, done, nil
	}
}

func scheduleOperations() ([]application.Definition, []application.Binding) {
	uses := func(names ...application.Name) []application.Name { return names }
	return []application.Definition{
			asstapp.DescribeScheduleCreate(), asstapp.DescribeScheduleUpdate(), asstapp.DescribeScheduleEnabled(),
			asstapp.DescribeScheduleCancel(), asstapp.DescribeScheduleList(), asstapp.DescribeScheduleHistory(), asstapp.DescribeScheduleRuns(),
			asstapp.DescribeScheduleAdd(), asstapp.DescribeScheduleRunNow(),
		}, append(scheduleAPIBindings(nil), []application.Binding{
			{Kind: application.HTML, Target: "GET /scheduled", Uses: uses(asstapp.ScheduleListName, asstapp.ScheduleHistoryName)},
			{Kind: application.HTML, Target: "POST /scheduled/{job_id}/toggle", Uses: uses(asstapp.ScheduleEnabledName, asstapp.ScheduleListName, asstapp.ScheduleHistoryName)},
			{Kind: application.HTML, Target: "POST /scheduled/{job_id}/cancel", Uses: uses(asstapp.ScheduleCancelName, asstapp.ScheduleListName, asstapp.ScheduleHistoryName)},
			{Kind: application.HTML, Target: "POST /scheduled/{job_id}/schedule", Uses: uses(asstapp.ScheduleUpdateName, asstapp.ScheduleListName, asstapp.ScheduleHistoryName)},
			{Kind: application.Tool, Target: "schedule_create", Uses: uses(asstapp.ScheduleCreateName)},
			{Kind: application.CLI, Target: "schedule list", Uses: uses(asstapp.ScheduleListName)},
			{Kind: application.CLI, Target: "schedule create", Uses: uses(asstapp.ScheduleAddName)},
			{Kind: application.CLI, Target: "schedule edit", Uses: uses(asstapp.ScheduleUpdateName)},
			{Kind: application.CLI, Target: "schedule delete", Uses: uses(asstapp.ScheduleCancelName)},
			{Kind: application.CLI, Target: "schedule run", Uses: uses(asstapp.ScheduleRunNowName)},
			{Kind: application.Tool, Target: "schedule_update", Uses: uses(asstapp.ScheduleUpdateName)},
			{Kind: application.Tool, Target: "schedule_cancel", Uses: uses(asstapp.ScheduleCancelName)},
			{Kind: application.Tool, Target: "schedule_list", Uses: uses(asstapp.ScheduleListName)},
		}...)
}
