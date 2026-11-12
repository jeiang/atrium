package scheduler

import (
	"fmt"
	"strconv"
	"time"

	scheddef "github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	KeyMinInterval     = "agent_schedule_min_interval_seconds"
	KeyNightlySchedule = "agent_schedule_nightly"
	KeyWeeklyReview    = "agent_schedule_weekly_review"
	KeyDailyNudges     = "agent_schedule_daily_nudges"
	KeyTaskReminders   = "agent_schedule_task_reminders"
	KeyMemoryWork      = "agent_schedule_memory_work"
	KeyKnowledgeExport = "agent_schedule_knowledge_export"
)

func Settings(defs []scheddef.Definition) []settings.Def {
	minimum := settings.Value(settings.Advanced, settings.Def{
		Key: KeyMinInterval, Module: "assistant", Type: settings.Int, Default: "3600",
		Check: settings.Between(60, 604800),
	})
	var gapped []string
	for _, def := range defs {
		if def.PromptRun {
			gapped = append(gapped, def.ScheduleSetting)
		}
	}
	minimum.Peers = minimumIntervals(gapped)
	return append([]settings.Def{minimum}, ScheduleSettings(defs)...)
}

func ScheduleSettings(defs []scheddef.Definition) []settings.Def {
	out := make([]settings.Def, 0, len(defs))
	for _, def := range defs {
		s := settings.Value(settings.Advanced, settings.Def{Key: def.ScheduleSetting, Module: "assistant", Type: settings.String, Default: def.Schedule, Check: recurringSchedule})
		if def.PromptRun {
			s.Peers = minimumGap
		}
		out = append(out, s)
	}
	return out
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

func minimumIntervals(keys []string) func(string, func(string) (string, error)) error {
	return func(value string, peer func(string) (string, error)) error {
		floor, err := strconv.Atoi(value)
		if err != nil {
			return settings.Invalid("must be a number")
		}
		for _, key := range keys {
			text, err := peer(key)
			if err != nil {
				return err
			}
			spec, err := Parse(text, "UTC")
			if err != nil {
				return settings.Invalid(err.Error())
			}
			if gap, ok := spec.MinGap(float64(time.Now().Unix())); ok && gap < float64(floor) {
				return settings.Invalid(fmt.Sprintf("%s must run at least %s seconds apart", key, value))
			}
		}
		return nil
	}
}
