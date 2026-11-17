package scheduler

import (
	"github.com/jeiang/atrium/internal/settings"
)

const KeyMinInterval = "agent_schedule_min_interval_seconds"

func Settings(module string) []settings.Def {
	return []settings.Def{settings.Value(settings.Advanced, settings.Def{
		Key: KeyMinInterval, Label: "Shortest schedule interval (seconds)", Module: module, Type: settings.Int, Default: "3600",
		Help:  "Applies when a schedule is created or changed; existing schedules keep running as they are.",
		Check: settings.Between(60, 604800),
	})}
}

func RecurringSchedule(value string) error {
	spec, err := Parse(value, "UTC")
	if err != nil {
		return settings.Invalid(err.Error())
	}
	if !spec.Recurring() {
		return settings.Invalid("must be a recurring schedule")
	}
	return nil
}
