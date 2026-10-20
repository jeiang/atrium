package runner

import (
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/skills"
	"github.com/jeiang/atrium/internal/assistant/subagents/research"
	profiletools "github.com/jeiang/atrium/internal/assistant/tools/profile"
	"github.com/jeiang/atrium/internal/assistant/toolset"
)

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
