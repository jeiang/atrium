package scheduler

import (
	"github.com/jeiang/atrium/internal/settings"
)

const (
	KeyTickSeconds     = "agent_scheduler_tick_seconds"
	KeyLeaseSeconds    = "agent_scheduler_lease_seconds"
	KeyMissedGrace     = "agent_scheduler_missed_grace_seconds"
	KeyMinInterval     = "agent_schedule_min_interval_seconds"
	KeyMaxActiveJobs   = "agent_schedule_max_active_jobs"
	KeyNightlySchedule = "agent_schedule_nightly"
	KeyWeeklyReview    = "agent_schedule_weekly_review"
	KeyDailyNudges     = "agent_schedule_daily_nudges"
	KeyCalendarCands   = "agent_schedule_calendar_candidates"
	KeyFXRates         = "agent_schedule_fx_rates"
	KeyMediaChannels   = "agent_schedule_media_channels"
	KeyMediaSweep      = "agent_schedule_media_sweep"
	KeyMediaThumbnails = "agent_schedule_media_thumbnails"
)

var scheduleKeys = map[string]string{
	NightlyName:            KeyNightlySchedule,
	"weekly-review":        KeyWeeklyReview,
	"daily-nudges":         KeyDailyNudges,
	CalendarCandidatesName: KeyCalendarCands,
	FXRatesName:            KeyFXRates,
	MediaChannelsName:      KeyMediaChannels,
	MediaSweepName:         KeyMediaSweep,
	MediaThumbnailsName:    KeyMediaThumbnails,
}

func init() {
	instance := []settings.Scope{settings.Instance}
	adv := func(key string, kind settings.Type, class settings.Class, value string, check func(string) error) settings.Def {
		return settings.Def{Key: key, Module: "assistant", Type: kind, Default: value, Check: check, Class: class, Scopes: instance}
	}
	timing := func(key, value string, lo, hi float64) settings.Def {
		return adv(key, settings.Int, settings.Advanced, value, settings.Between(lo, hi))
	}
	tick := timing(KeyTickSeconds, "30", 5, 3600)
	tick.Restart = true
	recurring := func(key, value string) settings.Def {
		return adv(key, settings.String, settings.Advanced, value, recurringSchedule)
	}
	settings.MustRegister(
		tick,
		timing(KeyLeaseSeconds, "1800", 60, 86400),
		timing(KeyMissedGrace, "120", 0, 86400),
		timing(KeyMinInterval, "3600", 60, 604800),
		adv(KeyMaxActiveJobs, settings.Int, settings.Cap, "20", settings.Between(1, 200)),
		recurring(KeyNightlySchedule, "daily 02:30"),
		recurring(KeyWeeklyReview, "weekly sun 18:00"),
		recurring(KeyDailyNudges, "daily 08:00"),
		recurring(KeyCalendarCands, "0 * * * *"),
		recurring(KeyFXRates, "weekly mon 06:00"),
		recurring(KeyMediaChannels, "*/30 * * * *"),
		recurring(KeyMediaSweep, "*/10 * * * *"),
		recurring(KeyMediaThumbnails, "daily 04:00"),
	)
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
