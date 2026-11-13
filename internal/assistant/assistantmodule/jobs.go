package assistantmodule

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/digest"
	"github.com/jeiang/atrium/internal/assistant/dreaming"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/assistant/memory/memorycmd"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	kbapp "github.com/jeiang/atrium/internal/kb/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
	tasksapp "github.com/jeiang/atrium/internal/tasks/application"
)

const weeklyReviewPrompt = "Weekly review for the operator. " + prompts.Untrusted + " Gather, then write the review as your reply, with these " +
	"short sections and nothing invented: " +
	"1) Wins: commitments marked done (commitments status=done) and things finished this week that you can " +
	"see in recent threads. " +
	"2) Waiting on them: open_loops state=waiting_on_them. " +
	"3) Stalled: items from open_loops or commitments (status=open) that have been open for more than 14 days. " +
	"4) Owed replies: open_loops state=owe_reply. " +
	"5) Commitments due in the next 7 days or overdue: commitments status=open, compared with today's date. " +
	"6) Next week: upcoming_birthdays days=7 and the calendar for the next 7 days. " +
	"7) Packages in transit: packages active_only=true. " +
	"8) Knowledge: call kb_search for records written or retracted this week and list them. " +
	"Call the attention tool for the cross-queue view (overdue, due soon, needs decision, waiting on them, upcoming) and use it for sections 2-7. " +
	"Cite message ids for each item so the operator can open the source. Say plainly when a section is empty " +
	"or when a date is only inferred. Finish by calling notify exactly once with a title and a two-line summary " +
	"of the most important items."

func assistantJob(name, kind, prompt, schedule, setting string, timeout time.Duration, spend scheduler.Spend) scheduler.Definition {
	return scheduler.Definition{
		Module: module.Assistant, Key: module.Assistant + "/" + name, Name: name, Kind: kind, Prompt: prompt,
		Schedule: schedule, ScheduleSetting: setting, Timeout: timeout, Spend: spend, Needs: []string{module.Assistant},
	}
}

func Jobs(deps jobworker.Dependencies) []scheduler.Definition {
	nightly := assistantJob(assistant.NightlyName, scheduler.JobNightly, "Nightly: gather the last day and write knowledge records (one model run).",
		"daily 02:30", assistant.KeyNightlySchedule, 45*time.Minute, scheduler.AlwaysSpends)
	nightly.Run = deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
		runs, err := dreaming.Run(ctx, e.Service, e.Now, e.Claim)
		return jobworker.Result{Runs: runs}, err
	})
	nightly.Recover = deps.Recover(dreaming.Admit)
	weekly := assistantJob(assistant.WeeklyReviewName, scheduler.JobBuiltin, weeklyReviewPrompt,
		"weekly sun 18:00", assistant.KeyWeeklyReview, 30*time.Minute, scheduler.AlwaysSpends)
	weekly.PromptRun = true
	weekly.Run = deps.Prompt(weeklyReviewPrompt)
	weekly.Needs = nil
	nudges := assistantJob(assistant.DailyNudgesName, scheduler.JobBuiltin, "Daily: send the attention digest (no model run).",
		"daily 08:00", assistant.KeyDailyNudges, 5*time.Minute, scheduler.ModelFree)
	nudges.Run = deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
		_, err := digest.Run(ctx, e.Service, e.Now, e.JobRunID, deps.Sources(ctx))
		return jobworker.Result{}, err
	})
	reminders := assistantJob(assistant.TaskRemindersName, scheduler.JobBuiltin, "Every 5 minutes: send due task reminders (no model run).",
		"*/5 * * * *", assistant.KeyTaskReminders, 5*time.Minute, scheduler.ModelFree)
	reminders.Run = deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
		_, err := tasksapp.New(e.Service.DB, e.Service.Runner.Ledger, e.Now).WithEnv(e.Service.Env).Remind(ctx, tasksapp.RemindInput{})
		return jobworker.Result{}, err
	})
	knowledgeExport := assistantJob(assistant.KnowledgeExportName, scheduler.JobBuiltin,
		"Daily: project the knowledge base into the configured export targets (no model run).",
		"daily 03:30", assistant.KeyKnowledgeExport, 30*time.Minute, scheduler.ModelFree)
	knowledgeExport.Run = deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
		_, err := kbapp.New(e.Service.DB, e.Service.Runner.Ledger, e.Now).WithEnv(e.Service.Env).Export(ctx, kbapp.ExportInput{})
		return jobworker.Result{}, err
	})
	return []scheduler.Definition{nightly, weekly, nudges, reminders, memorycmd.Job(deps), knowledgeExport}
}
