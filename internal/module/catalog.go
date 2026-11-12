package module

import "github.com/jeiang/atrium/internal/settings"

type Catalog struct {
	decls    []Declaration
	optional []string
	settings *settings.Registry
}

func (c *Catalog) Declarations() []Declaration { return c.decls }

func (c *Catalog) Optional() []string { return c.optional }

func (c *Catalog) Settings() *settings.Registry { return c.settings.Clone() }
