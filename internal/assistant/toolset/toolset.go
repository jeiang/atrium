// Package toolset is tools/__init__.py's MODULES: the production tool registry, every tool package's Tools()
// in one registry, and the undoers those packages register. A tool slice adds its package here; the loop
// narrows the set per run kind.
package toolset

import (
	"github.com/jeiang/atrium/internal/assistant/nightly"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/tool"
	contexttools "github.com/jeiang/atrium/internal/assistant/tools/context"
	mailtools "github.com/jeiang/atrium/internal/assistant/tools/mail"
	plantools "github.com/jeiang/atrium/internal/assistant/tools/plan"
	profiletools "github.com/jeiang/atrium/internal/assistant/tools/profile"
	proposetools "github.com/jeiang/atrium/internal/assistant/tools/propose"
	recalltools "github.com/jeiang/atrium/internal/assistant/tools/recall"
	scheduletools "github.com/jeiang/atrium/internal/assistant/tools/schedule"
	skillstools "github.com/jeiang/atrium/internal/assistant/tools/skills"
	"github.com/jeiang/atrium/internal/assistant/webfetch"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/notify"
)

// Registry is the registry every run's tools come from.
func Registry() (*registry.Registry, error) {
	r := registry.New()
	for _, tools := range [][]tool.Tool{
		mailtools.Tools(), notify.Tools(), webfetch.Tools(), nightly.Tools(),
		contexttools.Tools(), recalltools.Tools(), plantools.Tools(), profiletools.Tools(), proposetools.Tools(),
		scheduletools.Tools(), skillstools.Tools(),
	} {
		if err := r.Add(tools...); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Undoers is the writes registry of every undoer the tool packages define.
func Undoers() *writes.Registry {
	r := writes.NewRegistry()
	profiletools.RegisterUndoer(r)
	return r
}
