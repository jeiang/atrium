package assistantmodule

import (
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/unit"
)

func jobOperations() ([]application.Definition, []application.Binding) {
	job := func(name string, op application.Name) application.Binding {
		return application.Binding{Kind: application.Job, Target: unit.Assistant + "/" + name, Uses: []application.Name{op}}
	}
	return asstapp.DescribeJobs(), []application.Binding{
		job(assistant.NightlyName, asstapp.DreamName),
		job(assistant.DailyNudgesName, asstapp.DigestName),
		job(assistant.WeeklyReviewName, asstapp.ScheduleRunName),
	}
}
