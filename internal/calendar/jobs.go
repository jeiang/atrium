package calendar

import (
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

const (
	JobCandidates         = "calendar-candidates"
	KeyScheduleCandidates = "agent_schedule_calendar_candidates"
)

func Jobs() []scheduler.Definition {
	return []scheduler.Definition{{
		Key:             JobCandidates,
		Schedule:        "0 * * * *",
		ScheduleSetting: KeyScheduleCandidates,
		Needs:           []string{module.Calendar, module.Mail},
	}}
}
