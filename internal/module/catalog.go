package module

import (
	"context"
	"slices"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settingspage"
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

func (c *Catalog) Context(ctx context.Context) context.Context {
	ctx = connections.With(ctx, c.conns)
	return proposals.WithHooks(ctx, c.hooks)
}
