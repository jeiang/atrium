package module

import (
	"slices"

	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settingspage"
)

type Catalog struct {
	decls    []Declaration
	optional []string
	settings *settings.Registry
	sections settingspage.Sections
}

func (c *Catalog) Declarations() []Declaration { return c.decls }

func (c *Catalog) Optional() []string { return c.optional }

func (c *Catalog) Settings() *settings.Registry { return c.settings.Clone() }

func (c *Catalog) Sections() settingspage.Sections { return slices.Clone(c.sections) }

func (c *Catalog) SectionModules() map[string]bool { return c.sections.Modules() }
