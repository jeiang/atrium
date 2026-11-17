package signalsmodule

import (
	"slices"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/signals"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct{}

func Bindings() []application.Binding {
	loopsPage := []application.Name{OpenLoopsName, CommitmentsName}
	return append(apiRoutes(nil, nil), []application.Binding{
		{Kind: application.HTML, Target: "GET /loops", Uses: loopsPage},
		{Kind: application.HTML, Target: "POST /loops/{loop_id}/snooze", Uses: append([]application.Name{LoopSnoozeName}, loopsPage...)},
		{Kind: application.HTML, Target: "POST /loops/{loop_id}/dismiss", Uses: append([]application.Name{LoopDismissName}, loopsPage...)},
		{Kind: application.HTML, Target: "POST /loops/dismiss-all", Uses: append([]application.Name{LoopsDismissMatchingName}, loopsPage...)},
		{Kind: application.HTML, Target: "POST /loops/commitments/{commitment_id}/{outcome}", Uses: append([]application.Name{CommitmentDoneName, CommitmentDismissName}, loopsPage...)},
		{Kind: application.Tool, Target: "open_loops", Uses: []application.Name{OpenLoopsName}},
	}...)
}

func (Module) Declare(module.Context) module.Declaration {
	return module.Declaration{
		Name:                  unit.Signals,
		Title:                 "Signals",
		Core:                  true,
		Packages:              []string{"internal/signals"},
		Tables:                []string{"commitments", "loop_overrides", "open_loops"},
		Operations:            Describe(),
		Bindings:              Bindings(),
		Tools:                 ToolDefinitions(),
		Attention:             []attention.Source{signals.Loops(), signals.Commitments()},
		Queues:                []ops.Queue{{Name: "open_loops", Count: signals.CountOpen}},
		CommunicationsRefresh: signals.Refresh,
		Effects: func(d module.EffectDeps) module.Effects {
			r := &signals.Reminders{DB: d.DB, Ledger: d.Ledger, Now: d.Now}
			return module.Effects{Proposals: r.Proposals(unit.Signals), Undo: slices.Concat(signals.Undo(), []ledger.UndoDefinition{attention.SnoozeUndoDefinition(attention.UndoCommitmentSnooze, d.Now)})}
		},
		Pages: []web.Page{{
			Module: func(r chi.Router, d *web.Deps) {
				svc := newService(d)
				apiRoutes(d, svc)
				pageRoutes(r, d, svc)
			},
			Templates: Templates(),
			Nav:       []web.NavItem{{Section: "review", Label: "Loops", URL: "/loops", Icon: "list-checks", Page: "loops", Order: 20}},
		}},
	}
}
