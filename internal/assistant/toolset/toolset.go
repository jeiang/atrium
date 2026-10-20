package toolset

import (
	"github.com/jeiang/atrium/internal/assistant/nightly"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/tool"
	calendartools "github.com/jeiang/atrium/internal/assistant/tools/calendar"
	contexttools "github.com/jeiang/atrium/internal/assistant/tools/context"
	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	mailtools "github.com/jeiang/atrium/internal/assistant/tools/mail"
	mediatools "github.com/jeiang/atrium/internal/assistant/tools/media"
	moneytools "github.com/jeiang/atrium/internal/assistant/tools/money"
	notiontools "github.com/jeiang/atrium/internal/assistant/tools/notion"
	plantools "github.com/jeiang/atrium/internal/assistant/tools/plan"
	profiletools "github.com/jeiang/atrium/internal/assistant/tools/profile"
	proposetools "github.com/jeiang/atrium/internal/assistant/tools/propose"
	recalltools "github.com/jeiang/atrium/internal/assistant/tools/recall"
	scheduletools "github.com/jeiang/atrium/internal/assistant/tools/schedule"
	skillstools "github.com/jeiang/atrium/internal/assistant/tools/skills"
	"github.com/jeiang/atrium/internal/assistant/webfetch"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/money"
	"github.com/jeiang/atrium/internal/notify"
)

func Registry() (*registry.Registry, error) {
	notiontools.RegisterProposalKinds()
	r := registry.New()
	for _, tools := range [][]tool.Tool{
		mailtools.Tools(), mediatools.Tools(), notify.Tools(), webfetch.Tools(), nightly.Tools(),
		contexttools.Tools(), recalltools.Tools(), plantools.Tools(), profiletools.Tools(), proposetools.Tools(),
		scheduletools.Tools(), skillstools.Tools(), moneytools.Tools(), notiontools.Tools(),
		grafanatools.Tools(), calendartools.Tools(),
	} {
		if err := r.Add(tools...); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func Undoers(env config.Env) *writes.Registry {
	r := writes.NewRegistry()
	profiletools.RegisterUndoer(r)
	calendartools.RegisterUndoers(r)
	mediatools.RegisterUndoer(r, env)
	notiontools.RegisterUndoers(r, env)
	money.RegisterUndoer(r)
	return r
}
