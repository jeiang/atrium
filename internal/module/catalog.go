package module

type Catalog struct {
	decls    []Declaration
	optional []string
}

func (c *Catalog) Declarations() []Declaration { return c.decls }

func (c *Catalog) Optional() []string { return c.optional }
