package tracking

import (
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

const (
	JobPackageTracking         = "package-tracking"
	KeySchedulePackageTracking = "agent_schedule_package_tracking"
)

func Jobs() []scheduler.Definition {
	return []scheduler.Definition{{
		Key:             JobPackageTracking,
		Schedule:        "0 * * * *",
		ScheduleSetting: KeySchedulePackageTracking,
		Needs:           []string{module.Mail},
	}}
}
