package runner

import (
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/subagents/research"
	"github.com/jeiang/atrium/internal/assistant/toolset"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/kb"
)

func New(tools registry.Source) (*loop.Runner, error) {
	reg, err := tools()
	if err != nil {
		return nil, err
	}
	r := &loop.Runner{Registry: reg, Undoers: toolset.Undoers(config.OS), Profile: kb.ProfileBlock}
	rs := research.New(r)
	if err := rs.Install(reg); err != nil {
		return nil, err
	}
	return r, nil
}
