package module

import (
	"context"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/day"
)

type DayProvider = day.Provider

type daySources = []day.Source

func compileDay(decls []Declaration) ([]day.Source, error) {
	kinds := claims{}
	var out []day.Source
	for _, d := range decls {
		if d.Day == nil {
			continue
		}
		declared := d.Day.Kinds()
		if len(declared) == 0 {
			return nil, &InvalidDeclaration{Module: d.Name, Field: "day"}
		}
		for _, kind := range declared {
			if strings.TrimSpace(kind) == "" {
				return nil, &InvalidDeclaration{Module: d.Name, Field: "day"}
			}
			if err := kinds.take("day entry kind", kind, d.Name); err != nil {
				return nil, err
			}
		}
		out = append(out, day.Source{Unit: d.Name, Provider: d.Day})
	}
	return out, nil
}

func (c *Catalog) DaySources() []day.Source { return slices.Clone(c.regs.day) }

func EnabledDaySources(ctx context.Context) []day.Source {
	s := SetFrom(ctx)
	if s == nil {
		return nil
	}
	snap := s.Current()
	if snap == nil {
		return nil
	}
	return slices.DeleteFunc(s.Catalog().DaySources(), func(src day.Source) bool { return !snap.Enabled(src.Unit) })
}
