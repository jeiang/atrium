package scheduler

import (
	"fmt"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/settings"
)

const (
	KeyTickSeconds        = "agent_scheduler_tick_seconds"
	KeyLeaseSeconds       = "agent_scheduler_lease_seconds"
	KeyMissedGrace        = "agent_scheduler_missed_grace_seconds"
	KeyMinInterval        = "agent_schedule_min_interval_seconds"
	KeyMaxActiveJobs      = "agent_schedule_max_active_jobs"
	KeyNightlySchedule    = "agent_schedule_nightly"
	KeyWeeklyReview       = "agent_schedule_weekly_review"
	KeyDailyNudges        = "agent_schedule_daily_nudges"
	KeyCalendarCands      = "agent_schedule_calendar_candidates"
	KeyMediaChannels      = "agent_schedule_media_channels"
	KeyMediaSweep         = "agent_schedule_media_sweep"
	KeyMediaThumbnails    = "agent_schedule_media_thumbnails"
	KeyTaskReminders      = "agent_schedule_task_reminders"
	KeyMemoryWork         = "agent_schedule_memory_work"
	KeyPackageTracking    = "agent_schedule_package_tracking"
	KeyKnowledgeExport    = "agent_schedule_knowledge_export"
	KeyOpsSamplerSchedule = "agent_schedule_ops_sampler"
)

func init() {
	adv := func(key string, kind settings.Type, value string, check func(string) error) settings.Def {
		return settings.Value(settings.Advanced, settings.Def{Key: key, Module: "assistant", Type: kind, Default: value, Check: check})
	}
	timing := func(key, value string, lo, hi float64) settings.Def {
		return adv(key, settings.Int, value, settings.Between(lo, hi))
	}
	tick := timing(KeyTickSeconds, "30", 5, 3600)
	minimum := timing(KeyMinInterval, "3600", 60, 604800)
	minimum.Peers = minimumIntervals
	defs := []settings.Def{
		tick,
		timing(KeyLeaseSeconds, "1800", 60, 86400),
		timing(KeyMissedGrace, "120", 0, 86400),
		minimum,
		timing(KeyMaxActiveJobs, "20", 1, 1000),
	}
	for _, b := range Builtins {
		def := adv(b.Setting, settings.String, b.Default, recurringSchedule)
		if b.Run == PromptRun {
			def.Peers = minimumGap
		}
		defs = append(defs, def)
	}
	settings.MustRegister(defs...)
}

func recurringSchedule(value string) error {
	spec, err := Parse(value, "UTC")
	if err != nil {
		return settings.Invalid(err.Error())
	}
	if !spec.Recurring() {
		return settings.Invalid("must be a recurring schedule")
	}
	return nil
}

func minimumGap(value string, peer func(string) (string, error)) error {
	spec, err := Parse(value, "UTC")
	if err != nil {
		return settings.Invalid(err.Error())
	}
	text, err := peer(KeyMinInterval)
	if err != nil {
		return err
	}
	floor, err := strconv.Atoi(text)
	if err != nil {
		return settings.Invalid("must be a number")
	}
	if gap, ok := spec.MinGap(float64(time.Now().Unix())); ok && gap < float64(floor) {
		return settings.Invalid(fmt.Sprintf("must run at least %s seconds apart (%s)", text, KeyMinInterval))
	}
	return nil
}

func minimumIntervals(value string, peer func(string) (string, error)) error {
	floor, err := strconv.Atoi(value)
	if err != nil {
		return settings.Invalid("must be a number")
	}
	for _, b := range Builtins {
		if b.Run != PromptRun {
			continue
		}
		text, err := peer(b.Setting)
		if err != nil {
			return err
		}
		spec, err := Parse(text, "UTC")
		if err != nil {
			return settings.Invalid(err.Error())
		}
		if gap, ok := spec.MinGap(float64(time.Now().Unix())); ok && gap < float64(floor) {
			return settings.Invalid(fmt.Sprintf("%s must run at least %s seconds apart", b.Setting, value))
		}
	}
	return nil
}
