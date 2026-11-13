package apischema

import (
	"cmp"
	"slices"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/module"
)

type reachArea struct {
	Area       application.Area   `json:"area"`
	Operations []application.Name `json:"operations"`
	Required   int                `json:"required"`
	Bound      int                `json:"bound"`
}

type reachReport struct {
	Areas         []reachArea `json:"areas"`
	TotalRequired int         `json:"total_required"`
	TotalBound    int         `json:"total_bound"`
}

func reach(cat *module.Catalog) reachReport {
	area := map[application.Name]application.Area{}
	for _, b := range cat.Bindings() {
		if b.API != nil {
			if _, ok := area[b.Uses[0]]; !ok {
				area[b.Uses[0]] = b.API.Area
			}
		}
	}
	byArea := map[application.Area]*reachArea{}
	for _, a := range application.Areas {
		byArea[a] = &reachArea{Area: a, Operations: []application.Name{}}
	}
	var report reachReport
	for _, op := range cat.Operations() {
		required := application.RequiresAPI(op.Authority)
		if required {
			report.TotalRequired++
		}
		a, ok := area[op.Name]
		if !ok {
			continue
		}
		row := byArea[a]
		row.Operations = append(row.Operations, op.Name)
		row.Bound++
		if required {
			row.Required++
			report.TotalBound++
		}
	}
	for _, a := range application.Areas {
		slices.Sort(byArea[a].Operations)
		report.Areas = append(report.Areas, *byArea[a])
	}
	slices.SortFunc(report.Areas, func(x, y reachArea) int { return cmp.Compare(x.Area, y.Area) })
	return report
}
