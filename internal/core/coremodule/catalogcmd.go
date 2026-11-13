package coremodule

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/web"
)

type catalogBinding struct {
	Kind   application.BindingKind `json:"kind"`
	Target string                  `json:"target"`
}

type catalogOperation struct {
	Module    string                  `json:"module"`
	Name      application.Name        `json:"name"`
	Kind      application.Kind        `json:"kind"`
	Input     string                  `json:"input"`
	Output    string                  `json:"output"`
	Authority []application.Authority `json:"authority"`
	Effect    application.Effect      `json:"effect"`
	Audit     bool                    `json:"audit"`
	Bindings  []catalogBinding        `json:"bindings"`
	APIRoutes []string                `json:"api_routes"`
}

type catalogUnbound struct {
	Module string                  `json:"module"`
	Kind   application.BindingKind `json:"kind"`
	Target string                  `json:"target"`
}

type catalogReport struct {
	Operations []catalogOperation `json:"operations"`
	Unbound    []catalogUnbound   `json:"unbound,omitempty"`
}

func catalogCommand(mc module.Context) *cli.Command {
	return &cli.Command{Name: "catalog", Help: "inspect the module catalog", Commands: []*cli.Command{{
		Name: "operations",
		Help: "list application operations and their bindings",
		Setup: func(a *cli.Args) cli.Handler {
			asJSON := a.Bool("json", cli.Opt{Help: "print JSON"})
			missing := a.Bool("missing-api", cli.Opt{Help: "list only operations without an API binding"})
			unbound := a.Bool("unbound", cli.Opt{Help: "also list mounted routes, commands, tools, jobs and services without a binding"})
			require := a.Bool("require-api", cli.Opt{Help: "exit 1 when an operation has no API binding"})
			return func(env *cli.Env) int {
				cat := mc.Catalog()
				report := catalogReport{Operations: catalogOperations(cat, *missing || *require)}
				if *unbound {
					found, err := catalogUnboundItems(env, cat)
					if err != nil {
						return cli.Fail(env, err)
					}
					report.Unbound = found
				}
				if *asJSON {
					out, err := json.MarshalIndent(report, "", "  ")
					if err != nil {
						return cli.Fail(env, err)
					}
					_, _ = fmt.Fprintln(env.Stdout, string(out))
				} else {
					printCatalog(env, report)
				}
				if *require && len(report.Operations) > 0 {
					return cli.ExitError
				}
				return cli.ExitOK
			}
		},
	}}}
}

func catalogOperations(cat *module.Catalog, missingAPI bool) []catalogOperation {
	bound := map[application.Name][]catalogBinding{}
	for _, b := range cat.Bindings() {
		for _, name := range b.Uses {
			bound[name] = append(bound[name], catalogBinding{Kind: b.Kind, Target: b.Target})
		}
	}
	out := []catalogOperation{}
	for _, op := range cat.Operations() {
		row := catalogOperation{
			Module: op.Module, Name: op.Name, Kind: op.Kind, Input: op.Input.String(), Output: op.Output.String(),
			Authority: op.Authority, Effect: op.Effect, Audit: op.Audit, Bindings: bound[op.Name], APIRoutes: []string{},
		}
		slices.SortFunc(row.Bindings, func(a, b catalogBinding) int {
			return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Target, b.Target))
		})
		for _, b := range row.Bindings {
			if b.Kind == application.API {
				row.APIRoutes = append(row.APIRoutes, b.Target)
			}
		}
		if missingAPI && len(row.APIRoutes) > 0 {
			continue
		}
		out = append(out, row)
	}
	return out
}

func catalogUnboundItems(env *cli.Env, cat *module.Catalog) ([]catalogUnbound, error) {
	var out []catalogUnbound
	if set := module.SetFrom(env.Ctx); set != nil {
		routes, err := web.Unbound(env.Ctx, set.Pages())
		if err != nil {
			return nil, err
		}
		for _, r := range routes {
			out = append(out, catalogUnbound{Module: r.Owner, Kind: r.Kind, Target: r.Method + " " + r.Pattern})
		}
	}
	for _, item := range cat.Unbound() {
		out = append(out, catalogUnbound{Module: item.Module, Kind: item.Kind, Target: item.Target})
	}
	return out, nil
}

func printCatalog(env *cli.Env, report catalogReport) {
	w := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "MODULE\tNAME\tKIND\tINPUT\tOUTPUT\tAUTHORITY\tEFFECT\tAUDIT\tBINDINGS")
	for _, op := range report.Operations {
		authority := make([]string, len(op.Authority))
		for i, a := range op.Authority {
			authority[i] = string(a)
		}
		targets := make([]string, len(op.Bindings))
		for i, b := range op.Bindings {
			targets[i] = string(b.Kind) + ":" + b.Target
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%t\t%s\n", op.Module, op.Name, op.Kind, op.Input, op.Output,
			strings.Join(authority, ","), op.Effect, op.Audit, strings.Join(targets, " "))
	}
	if len(report.Unbound) > 0 {
		_, _ = fmt.Fprintln(w, "\nUNBOUND\tKIND\tTARGET")
		for _, u := range report.Unbound {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", u.Module, u.Kind, u.Target)
		}
	}
	_ = w.Flush()
}
