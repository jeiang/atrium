package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	NightlyName = "nightly"

	WeeklyReviewName       = "weekly-review"
	DailyNudgesName        = "daily-nudges"
	CalendarCandidatesName = "calendar-candidates"
	MediaChannelsName      = "media-channels"
	MediaSweepName         = "media-sweep"
	MediaThumbnailsName    = "media-thumbnails"
	TaskRemindersName      = "task-reminders"
	MemoryWorkName         = "memory-work"
	PackageTrackingName    = "package-tracking"
	KnowledgeExportName    = "knowledge-export"
	OpsSamplerName         = "ops-sampler"
)

type RunKind uint8

const (
	PromptRun RunKind = iota + 1
	CallableRun
)

type Builtin struct {
	Name    string
	Setting string
	Default string
	Prompt  string
	Kind    string
	Run     RunKind
}

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

var Builtins = []Builtin{
	{NightlyName, KeyNightlySchedule, "daily 02:30", "Nightly: gather the last day and write knowledge records (one model run).", JobNightly, CallableRun},
	{WeeklyReviewName, KeyWeeklyReview, "weekly sun 18:00", weeklyReviewPrompt, JobBuiltin, PromptRun},
	{DailyNudgesName, KeyDailyNudges, "daily 08:00", "Daily: send the attention digest (no model run).", JobBuiltin, CallableRun},
	{OpsSamplerName, KeyOpsSamplerSchedule, "*/15 * * * *", "Every 15 minutes: sample host metrics and firing alerts into ops samples (no model run).", JobBuiltin, CallableRun},
	{CalendarCandidatesName, KeyCalendarCands, "0 * * * *", "Hourly: if event candidates are queued, draft calendar event proposals from them (no model run when the queue is empty).", JobBuiltin, CallableRun},
	{MediaChannelsName, KeyMediaChannels, "*/30 * * * *", "Every 30 minutes: poll EZTV for the media channels and add new matches to qBittorrent (no model run).", JobBuiltin, CallableRun},
	{MediaSweepName, KeyMediaSweep, "*/10 * * * *", "Every 10 minutes: select files in new media torrents and remove filed torrents qBittorrent has stopped (no model run).", JobBuiltin, CallableRun},
	{MediaThumbnailsName, KeyMediaThumbnails, "daily 04:00", "Daily: generate missing episode thumbnails for the media library (no model run).", JobBuiltin, CallableRun},
	{TaskRemindersName, KeyTaskReminders, "*/5 * * * *", "Every 5 minutes: send due task reminders (no model run).", JobBuiltin, CallableRun},
	{MemoryWorkName, KeyMemoryWork, "*/2 * * * *", "Every 2 minutes: summarize pending memory nodes (no model run when the queue is empty).", JobBuiltin, CallableRun},
	{PackageTrackingName, KeyPackageTracking, "0 * * * *", "Hourly: refresh active Ship24 package trackers (no model run).", JobBuiltin, CallableRun},
	{KnowledgeExportName, KeyKnowledgeExport, "daily 03:30", "Daily: project the knowledge base into the configured export targets (no model run).", JobBuiltin, CallableRun},
}

var builtinsByName = func() map[string]Builtin {
	m := make(map[string]Builtin, len(Builtins))
	for _, b := range Builtins {
		m[b.Name] = b
	}
	return m
}()

func BuiltinFor(name string) (Builtin, bool) {
	b, ok := builtinsByName[name]
	return b, ok
}

func builtinSchedule(ctx context.Context, q db.Querier, name string) (string, bool, error) {
	spec, ok := BuiltinFor(name)
	if !ok {
		return "", false, fmt.Errorf("unknown builtin %q", name)
	}
	found, err := settings.From(ctx).Resolve(ctx, q, spec.Setting)
	return found.Value, found.Origin != settings.FromDefault, err
}

func syncSchedule(ctx context.Context, q db.Querier, job Job, now float64) error {
	if _, ok := BuiltinFor(job.Name); !ok {
		return nil
	}
	schedule, explicit, err := builtinSchedule(ctx, q, job.Name)
	if err != nil || !explicit || schedule == job.Schedule {
		return err
	}
	_, err = UpdateJob(ctx, q, job.ID, now, JobUpdate{Schedule: &schedule, Actor: Operator})
	var refused Error
	if errors.As(err, &refused) {
		log.Warn(fmt.Sprintf("schedule setting for %s not applied: %s", job.Name, refused), logx.Job, job.ID)
		return nil
	}
	return err
}

func Ensure(ctx context.Context, q db.Querier, tz string, now float64) error {
	if err := ensureNightly(ctx, q, tz, now); err != nil {
		return err
	}
	return ensureBuiltins(ctx, q, tz, now)
}

func ensureNightly(ctx context.Context, q db.Querier, tz string, now float64) error {
	return ensureOne(ctx, q, tz, now, NightlyName)
}

func ensureBuiltins(ctx context.Context, q db.Querier, tz string, now float64) error {
	rows, err := q.QueryContext(ctx, "SELECT id, name FROM jobs WHERE kind = ?", JobBuiltin)
	if err != nil {
		return err
	}
	existing, err := db.Collect(rows, func(r *sql.Rows) (have, error) {
		var h have
		return h, r.Scan(&h.id, &h.name)
	})
	if err != nil {
		return err
	}
	for _, spec := range Builtins {
		if spec.Kind != JobBuiltin {
			continue
		}
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
		if err := createBuiltin(ctx, q, tz, now, spec); err != nil {
			return err
		}
	}
	return retireBuiltins(ctx, q, existing)
}

func ensureOne(ctx context.Context, q db.Querier, tz string, now float64, name string) error {
	spec, ok := BuiltinFor(name)
	if !ok {
		return fmt.Errorf("unknown builtin %q", name)
	}
	var id int64
	err := q.QueryRowContext(ctx, "SELECT id FROM jobs WHERE kind = ?", spec.Kind).Scan(&id)
	if err == nil {
		job, err := GetJob(ctx, q, id)
		if err != nil || job == nil {
			return err
		}
		return syncSchedule(ctx, q, *job, now)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return createBuiltin(ctx, q, tz, now, spec)
}

func createBuiltin(ctx context.Context, q db.Querier, tz string, now float64, spec Builtin) error {
	schedule, _, err := builtinSchedule(ctx, q, spec.Name)
	if err != nil {
		return err
	}
	_, err = CreateJob(ctx, q, NewJob{
		Name: spec.Name, Prompt: spec.Prompt, Schedule: schedule, TZ: tz, Now: now,
		CreatedBy: Operator, Kind: spec.Kind,
	})
	return err
}

type have struct {
	id   int64
	name string
}

func retireBuiltins(ctx context.Context, q db.Querier, existing []have) error {
	for _, h := range existing {
		if _, known := BuiltinFor(h.name); known {
			continue
		}
		if _, err := q.ExecContext(ctx, "UPDATE jobs SET state = ?, next_due = NULL WHERE id = ? AND state != ?", StateCancelled, h.id, StateCancelled); err != nil {
			return err
		}
	}
	return nil
}
