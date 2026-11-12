package module

import (
	"context"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settingspage"
	"github.com/jeiang/atrium/internal/tool"
)

type Catalog struct {
	decls      []Declaration
	optional   []string
	settings   *settings.Registry
	sections   settingspage.Sections
	conns      *connections.Registry
	hooks      proposals.Hooks
	credential config.AccountCredentialFunc
	metrics    []ops.Metric
	checks     []ops.Check
}

func (c *Catalog) Declarations() []Declaration { return c.decls }

func (c *Catalog) Optional() []string { return c.optional }

func (c *Catalog) Settings() *settings.Registry { return c.settings.Clone() }

func (c *Catalog) Sections() settingspage.Sections { return slices.Clone(c.sections) }

func (c *Catalog) SectionModules() map[string]bool { return c.sections.Modules() }

func (c *Catalog) Metrics() []ops.Metric { return slices.Clone(c.metrics) }

func (c *Catalog) Checks() []ops.Check { return slices.Clone(c.checks) }

func (c *Catalog) Connections() *connections.Registry { return c.conns }

func (c *Catalog) Hooks() proposals.Hooks { return c.hooks }

func (c *Catalog) AccountCredential() config.AccountCredentialFunc { return c.credential }

func (c *Catalog) ToolDocs() []tool.Tool {
	var out []tool.Tool
	for _, d := range c.decls {
		for _, def := range d.Tools {
			out = append(out, def.Tool)
		}
	}
	slices.SortFunc(out, func(a, b tool.Tool) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (c *Catalog) Approvals() []tool.Tool {
	var out []tool.Tool
	for _, decl := range c.decls {
		for _, def := range decl.Tools {
			if def.Tool.Approval != nil {
				out = append(out, def.Tool)
			}
		}
	}
	return out
}

func (c *Catalog) ProposeKinds() []proposals.Definition {
	var out []proposals.Definition
	for _, decl := range c.decls {
		out = append(out, decl.ProposeKinds...)
	}
	return out
}

func (c *Catalog) Ledger(d *db.DB, env config.Env, open mail.OpenFunc, now func() float64) (*ledger.Store, error) {
	return ledger.NewStoreWith(d, now, func(s *ledger.Store) ([]ledger.UndoDefinition, []ledger.ResolveDefinition, error) {
		var undo []ledger.UndoDefinition
		var resolve []ledger.ResolveDefinition
		for _, decl := range c.decls {
			if decl.Effects != nil {
				effects := decl.Effects(EffectDeps{DB: d, Ledger: s, Env: env, Open: open, Now: now})
				undo = append(undo, effects.Undo...)
				resolve = append(resolve, effects.Resolve...)
			}
		}
		return undo, resolve, nil
	})
}

type UnknownProposalKind struct{ Kind string }

func (e *UnknownProposalKind) Error() string { return "unknown proposal kind " + e.Kind }

func (c *Catalog) Proposals(deps EffectDeps) (*proposals.Registry, error) {
	var defs []proposals.Definition
	var sync proposals.SyncHook
	var syncModule string
	for _, decl := range c.decls {
		if decl.Effects == nil {
			continue
		}
		effects := decl.Effects(deps)
		defs = append(defs, effects.Proposals...)
		if effects.Sync != nil {
			if sync != nil {
				return nil, &DuplicateHook{Hook: "Sync", Modules: []string{syncModule, decl.Name}}
			}
			sync, syncModule = effects.Sync, decl.Name
		}
	}
	r, err := proposals.NewRegistryFrom(defs, sync)
	if err != nil {
		return nil, err
	}
	for _, decl := range c.decls {
		for _, kind := range decl.ProposeKinds {
			if r.Executors[kind.Kind.Name] == nil {
				return nil, &UnknownProposalKind{Kind: kind.Kind.Name}
			}
		}
	}
	return r, nil
}

func (c *Catalog) Context(ctx context.Context) context.Context {
	ctx = connections.With(ctx, c.conns)
	return proposals.WithHooks(ctx, c.hooks)
}
