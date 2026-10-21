package runner

import (
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/skills"
	"github.com/jeiang/atrium/internal/assistant/subagents/research"
	profiletools "github.com/jeiang/atrium/internal/assistant/tools/profile"
)

func New(tools registry.Source) (*loop.Runner, error) {
	reg, err := tools()
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
