package assistantmodule

import (
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	commitmentsapp "github.com/jeiang/atrium/internal/commitments/application"
	tasksapp "github.com/jeiang/atrium/internal/tasks/application"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/unit"
)

func commitmentToolService(tc *tool.Context) *commitmentsapp.Service {
	return commitmentsapp.New(tc.DB, tc.Ledger, tc.Now)
}

func taskToolService(tc *tool.Context) *tasksapp.Service {
	return tasksapp.New(tc.DB, tc.Ledger, tc.Now)
}

func (m Module) taskOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			tasksapp.DescribeList(), tasksapp.DescribeGet(), tasksapp.DescribeAdd(), tasksapp.DescribeUpdate(), tasksapp.DescribeDone(),
			tasksapp.DescribeReopen(), tasksapp.DescribeDismiss(), tasksapp.DescribeDelete(), tasksapp.DescribeRemind(), tasksapp.DescribeProposeReminder(),
		}, append(m.taskRoutes(nil), []application.Binding{
			{Kind: application.Tool, Target: "commitment_list", Uses: []application.Name{commitmentsapp.ListName}, Delegates: []application.Name{commitmentsapp.ListName}},
			{Kind: application.Tool, Target: "task_add", Uses: []application.Name{tasksapp.AddName}},
			{Kind: application.Tool, Target: "task_update", Uses: []application.Name{tasksapp.UpdateName}},
			{Kind: application.Tool, Target: "task_close", Uses: []application.Name{tasksapp.DoneName, tasksapp.DismissName}},
			{Kind: application.Job, Target: unit.Assistant + "/" + assistant.TaskRemindersName, Uses: []application.Name{tasksapp.RemindName}},
		}...)
}
