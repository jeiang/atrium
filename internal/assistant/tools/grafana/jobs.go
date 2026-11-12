package grafanatools

import (
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

const (
	JobSampler         = "ops-sampler"
	KeyScheduleSampler = "agent_schedule_ops_sampler"
)

func Jobs() []scheduler.Definition {
	return []scheduler.Definition{{
		Key:             JobSampler,
		Schedule:        "*/15 * * * *",
		ScheduleSetting: KeyScheduleSampler,
		Needs:           []string{module.Grafana},
	}}
}
