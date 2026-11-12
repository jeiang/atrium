package module

import (
	"context"
	"database/sql"
	"encoding/json"
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

func (c *Catalog) HostAccounts() config.HostAccountsFunc {
	for _, d := range c.decls {
		if d.AccountSync != nil {
			return d.AccountSync.Accounts
		}
	}
	return nil
}

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
		operations := map[string]string{}
		kinds := map[string]string{}
		s.BindEffect = func(ctx context.Context, q db.Querier, effect ledger.Effect) (context.Context, error) {
			owner := operations[effect.Operation]
			if owner == "" && effect.ProposalID != nil {
				kind, err := proposals.KindOf(ctx, q, *effect.ProposalID)
				if err != nil {
					return ctx, err
				}
				owner = kinds[kind]
			}
			if owner == "" && len(effect.Undo) > 0 {
				var undo struct {
					Op string `json:"op"`
				}
				if err := json.Unmarshal(effect.Undo, &undo); err != nil {
					return ctx, err
				}
				owner = operations[undo.Op]
			}
			if owner == "" {
				switch effect.Origin {
				case "mail_auto", "operator_send":
					owner = Mail
				case "calendar":
					owner = Calendar
				case "media":
					owner = Media
				case "notify":
					owner = Notifications
				case "export", "agent", "local":
					owner = Assistant
				}
			}
			if owner == "" {
				return ctx, ledger.Refused("the effect has no registered owner")
			}
			return bindOwner(ctx, owner, q)
		}
		var undo []ledger.UndoDefinition
		var resolve []ledger.ResolveDefinition
		for _, decl := range c.decls {
			for _, def := range decl.Tools {
				operations[def.Tool.Name] = decl.Name
			}
			if decl.Effects != nil {
				effects := decl.Effects(EffectDeps{DB: d, Ledger: s, Env: env, Open: open, Now: now})
				for _, def := range effects.Proposals {
					kinds[def.Kind.Name] = decl.Name
				}
				for _, def := range effects.Undo {
					operations[def.Operation] = decl.Name
					def.Undoer.Bind = func(ctx context.Context) (context.Context, error) {
						return bindOwner(ctx, decl.Name, d.Read)
					}
					undo = append(undo, def)
				}
				for _, def := range effects.Resolve {
					operations[def.Operation] = decl.Name
					hook := def.Hook
					def.Hook = func(ctx context.Context, tx *sql.Tx, effect ledger.Effect, resolution ledger.Resolution) error {
						ctx, err := bindOwner(ctx, decl.Name, tx)
						if err != nil {
							return err
						}
						if err := ledger.CheckAdmission(ctx); err != nil {
							return err
						}
						return hook(ctx, tx, effect, resolution)
					}
					resolve = append(resolve, def)
				}
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
		for _, def := range effects.Proposals {
			execute := def.Execute
			if execute != nil {
				def.Execute = func(ctx context.Context, prop *proposals.Proposal, selection any) (any, error) {
					ctx, err := bindOwner(ctx, decl.Name, deps.DB.Read)
					if err != nil {
						return nil, err
					}
					return execute(ctx, prop, selection)
				}
			}
			defs = append(defs, def)
		}
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
	r.Bind = func(ctx context.Context, kind string) (context.Context, error) {
		def, ok := r.Kind(kind)
		if !ok {
			return ctx, &UnknownProposalKind{Kind: kind}
		}
		return bindOwner(ctx, def.Module, deps.DB.Read)
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
