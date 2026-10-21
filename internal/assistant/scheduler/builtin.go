package scheduler

import (
	"context"
	"slices"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/nightly"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/db"
)

const (
	NightlyName = "nightly"

	CalendarCandidatesName = "calendar-candidates"
	FXRatesName            = "fx-rates"
	MediaChannelsName      = "media-channels"
	MediaSweepName         = "media-sweep"
	MediaThumbnailsName    = "media-thumbnails"
)

type Builtin struct {
	Name, Prompt string
}

const weeklyReviewPrompt = "Weekly review for the operator. " + prompts.Untrusted + " Gather, then write the review as your reply, with these " +
	"short sections and nothing invented: " +
	"1) Wins: commitments marked done (commitments status=done) and things finished this week that you can " +
	"see in recent threads. " +
	"2) Waiting on them: open_loops state=waiting_on_them. " +
	"3) Stalled: items from open_loops or commitments (status=open) that have been open for more than 14 days. " +
	"4) Owed replies: open_loops state=owe_reply. " +
	"5) Commitments due in the next 7 days or overdue: commitments status=open, compared with today's date. " +
	"6) Next week: bills upcoming_days=7, upcoming_birthdays days=7, and the calendar for the next 7 days. " +
	"7) Packages in transit: packages active_only=true. " +
	"Cite message ids for each item so the operator can open the source. Say plainly when a section is empty " +
	"or when a date is only inferred. Finish by calling notify exactly once with a title and a two-line summary " +
	"of the most important items."

const dailyNudgesPrompt = "Daily nudges for the operator. " + prompts.Untrusted + " Check, using today's date: " +
	"upcoming_birthdays days=3 (birthdays and anniversaries), bills upcoming_days=7, and commitments " +
	"status=open whose due date is before today (overdue). If NONE of these has an item, reply with the single " +
	"line 'Nothing to nudge.' and do not call notify. Otherwise call notify exactly once with a short title and " +
	"a plain list (one line per item: who or what, the date, and the source message id when there is one), " +
	"then reply with the same list."

var BuiltinJobs = []Builtin{
	{"weekly-review", weeklyReviewPrompt},
	{"daily-nudges", dailyNudgesPrompt},
	{CalendarCandidatesName, "Hourly: if event candidates are queued, draft calendar event proposals from them (no model run when the queue is empty)."},
	{FXRatesName, "Weekly: fetch the CBTT USD selling rate for the spending ledger (no model run)."},
	{MediaChannelsName, "Every 30 minutes: poll EZTV for the media channels and add new matches to qBittorrent (no model run)."},
	{MediaSweepName, "Every 10 minutes: select files in new media torrents and remove filed torrents qBittorrent has stopped (no model run)."},
	{MediaThumbnailsName, "Daily: generate missing episode thumbnails for the media library (no model run)."},
}

func builtinSchedule(ctx context.Context, q db.Querier, name string) (string, error) {
	r := assistant.Read(ctx, q)
	schedule := r.Text(scheduleKeys[name])
	return schedule, r.Err()
}

var CallableNames = []string{CalendarCandidatesName, FXRatesName, MediaChannelsName, MediaSweepName, MediaThumbnailsName}

func BuiltinPrompt(name string) (string, bool) {
	for _, b := range BuiltinJobs {
		if b.Name == name {
			return b.Prompt, true
		}
	}
	return "", false
}

func EnsureNightly(ctx context.Context, q db.Querier, tz string, now float64) error {
	var one int
	err := q.QueryRowContext(ctx, "SELECT 1 FROM jobs WHERE kind = ?", JobNightly).Scan(&one)
	if err == nil {
		return nil
	}
	if !isNoRows(err) {
		return err
	}
	schedule, err := builtinSchedule(ctx, q, NightlyName)
	if err != nil {
		return err
	}
	_, err = CreateJob(ctx, q, NewJob{
		Name: NightlyName, Prompt: nightly.Prompt, Schedule: schedule, TZ: tz, Now: now,
		CreatedBy: Operator, Kind: JobNightly, SkipLimits: true,
	})
	return err
}

func EnsureBuiltins(ctx context.Context, q db.Querier, tz string, now float64) error {
	rows, err := q.QueryContext(ctx, "SELECT name FROM jobs WHERE kind = ?", JobBuiltin)
	if err != nil {
		return err
	}
	have, err := db.Collect(rows, func(r *sqlRows) (string, error) {
		var name string
		return name, r.Scan(&name)
	})
	if err != nil {
		return err
	}
	for _, spec := range BuiltinJobs {
		if slices.Contains(have, spec.Name) {
			continue
		}
		schedule, err := builtinSchedule(ctx, q, spec.Name)
		if err != nil {
			return err
		}
		if _, err := CreateJob(ctx, q, NewJob{
			Name: spec.Name, Prompt: spec.Prompt, Schedule: schedule, TZ: tz, Now: now,
			CreatedBy: Operator, Kind: JobBuiltin, SkipLimits: true,
		}); err != nil {
			return err
		}
	}
	return nil
}
