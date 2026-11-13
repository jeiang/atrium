package assistantmodule

import (
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/module"
	tasksapp "github.com/jeiang/atrium/internal/tasks/application"
	"github.com/jeiang/atrium/internal/tool"
)

func taskToolService(tc *tool.Context) *tasksapp.Service {
	return tasksapp.New(tc.DB, tc.Ledger, tc.Now)
}

func taskOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			tasksapp.DescribeList(), tasksapp.DescribeAdd(), tasksapp.DescribeUpdate(), tasksapp.DescribeDone(), tasksapp.DescribeRemind(), tasksapp.DescribeProposeReminder(),
		}, []application.Binding{
			{Kind: application.Tool, Target: "task_list", Uses: []application.Name{tasksapp.ListName}},
			{Kind: application.Tool, Target: "task_add", Uses: []application.Name{tasksapp.AddName}},
			{Kind: application.Tool, Target: "task_update", Uses: []application.Name{tasksapp.UpdateName}},
			{Kind: application.Tool, Target: "task_done", Uses: []application.Name{tasksapp.DoneName}},
			{Kind: application.Job, Target: module.Assistant + "/" + assistant.TaskRemindersName, Uses: []application.Name{tasksapp.RemindName}},
		}
}
