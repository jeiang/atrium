package scheduler

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jeiang/atrium/internal/assistant/nightly"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings"
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

func builtinSchedule(ctx context.Context, q db.Querier, name string) (string, bool, error) {
	found, err := settings.Default.Resolve(ctx, q, scheduleKeys[name], 0)
	return found.Value, found.Origin != settings.FromDefault, err
}

var modelRuns = map[string]bool{NightlyName: true, "weekly-review": true, "daily-nudges": true}

func syncSchedule(ctx context.Context, q db.Querier, job Job, now float64) error {
	schedule, explicit, err := builtinSchedule(ctx, q, job.Name)
	if err != nil || !explicit || schedule == job.Schedule {
		return err
	}
	_, err = UpdateJob(ctx, q, job.ID, now, JobUpdate{Schedule: &schedule, SkipLimits: !modelRuns[job.Name]})
	var refused Error
	if errors.As(err, &refused) {
		log.Warn(fmt.Sprintf("schedule setting for %s not applied: %s", job.Name, refused), logx.Job, job.ID)
		return nil
	}
	return err
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
	var id int64
	err := q.QueryRowContext(ctx, "SELECT id FROM jobs WHERE kind = ?", JobNightly).Scan(&id)
	if err == nil {
		job, err := GetJob(ctx, q, id)
		if err != nil || job == nil {
			return err
		}
		return syncSchedule(ctx, q, *job, now)
	}
	if !isNoRows(err) {
		return err
	}
	schedule, _, err := builtinSchedule(ctx, q, NightlyName)
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
	rows, err := q.QueryContext(ctx, "SELECT id, name FROM jobs WHERE kind = ?", JobBuiltin)
	if err != nil {
		return err
	}
	type have struct {
		id   int64
		name string
	}
	existing, err := db.Collect(rows, func(r *sqlRows) (have, error) {
		var h have
		return h, r.Scan(&h.id, &h.name)
	})
	if err != nil {
		return err
	}
	for _, spec := range BuiltinJobs {
		if i := slices.IndexFunc(existing, func(h have) bool { return h.name == spec.Name }); i >= 0 {
			job, err := GetJob(ctx, q, existing[i].id)
			if err != nil {
				return err
			}
			if job != nil {
				if err := syncSchedule(ctx, q, *job, now); err != nil {
					return err
				}
			}
			continue
		}
		schedule, _, err := builtinSchedule(ctx, q, spec.Name)
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
