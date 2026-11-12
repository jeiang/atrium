package assistantmodule

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/assistant/digest"
	"github.com/jeiang/atrium/internal/assistant/dreaming"
	"github.com/jeiang/atrium/internal/assistant/memory/memorycmd"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/kb/export"
	"github.com/jeiang/atrium/internal/module"
	scheddef "github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/tasks/remind"
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

func assistantJob(name, kind, prompt, schedule, setting string, timeout time.Duration, spend scheddef.Spend) scheddef.Definition {
	return scheddef.Definition{
		Module: module.Assistant, Key: module.Assistant + "/" + name, Name: name, Kind: kind, Prompt: prompt,
		Schedule: schedule, ScheduleSetting: setting, Timeout: timeout, Spend: spend, Needs: []string{module.Assistant},
	}
}

func Jobs(deps worker.Dependencies) []scheddef.Definition {
	nightly := assistantJob(scheduler.NightlyName, scheduler.JobNightly, "Nightly: gather the last day and write knowledge records (one model run).",
		"daily 02:30", scheduler.KeyNightlySchedule, 45*time.Minute, scheddef.AlwaysSpends)
	nightly.Run = deps.Callable(func(ctx context.Context, e worker.Env) (worker.Result, error) {
		runs, err := dreaming.Run(ctx, e.Service, e.Now, e.JobID, e.JobRunID)
		return worker.Result{Runs: runs}, err
	})
	weekly := assistantJob(scheduler.WeeklyReviewName, scheduler.JobBuiltin, weeklyReviewPrompt,
		"weekly sun 18:00", scheduler.KeyWeeklyReview, 30*time.Minute, scheddef.AlwaysSpends)
	weekly.PromptRun = true
	weekly.Needs = nil
	nudges := assistantJob(scheduler.DailyNudgesName, scheduler.JobBuiltin, "Daily: send the attention digest (no model run).",
		"daily 08:00", scheduler.KeyDailyNudges, 5*time.Minute, scheddef.ModelFree)
	nudges.Run = deps.Callable(func(ctx context.Context, e worker.Env) (worker.Result, error) {
		_, err := digest.Run(ctx, e.Service, e.Now, e.JobRunID, deps.Sources(ctx))
		return worker.Result{}, err
	})
	reminders := assistantJob(scheduler.TaskRemindersName, scheduler.JobBuiltin, "Every 5 minutes: send due task reminders (no model run).",
		"*/5 * * * *", scheduler.KeyTaskReminders, 5*time.Minute, scheddef.ModelFree)
	reminders.Run = deps.Callable(func(ctx context.Context, e worker.Env) (worker.Result, error) {
		_, err := remind.Due(ctx, e.Service.DB, e.Service.Env, e.Now())
		return worker.Result{}, err
	})
	return []scheddef.Definition{nightly, weekly, nudges, reminders, memorycmd.Job(deps), export.Job(deps)}
}
