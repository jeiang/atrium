// Package runner is the one place the production *loop.Runner is composed: the tool registry (toolset plus the
// research tool bound to the runner that executes its children) and the hooks the loop calls (the skills index,
// /skill expansion and the parallel research batch). Every process that runs the assistant (serve, the
// scheduler, `agent run`) builds its Runner here and nowhere else.
package runner

import (
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/skills"
	"github.com/jeiang/atrium/internal/assistant/subagents/research"
	profiletools "github.com/jeiang/atrium/internal/assistant/tools/profile"
	"github.com/jeiang/atrium/internal/assistant/toolset"
)

// New is the production Runner.
func New() (*loop.Runner, error) {
	reg, err := toolset.Registry()
	if err != nil {
		return nil, err
	}
	r := &loop.Runner{Registry: reg, Profile: profiletools.Block, SkillsIndex: skills.IndexBlock, ExpandSkills: skills.ExpandHook}
	rs := research.New(r)
	if err := rs.Install(reg); err != nil {
		return nil, err
	}
	r.ResearchBatch = rs.Batch
	return r, nil
}
