package coremodule

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/jeiang/atrium/internal/apischema"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/web"
)

type catalogBinding struct {
	Kind      application.BindingKind `json:"kind"`
	Target    string                  `json:"target"`
	Delegates []application.Name      `json:"delegates,omitempty"`
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
	Area      application.Area        `json:"area,omitempty"`
	Bindings  []catalogBinding        `json:"bindings"`
	APIRoutes []string                `json:"api_routes"`
}

type catalogUnbound struct {
	Module string                  `json:"module"`
	Kind   application.BindingKind `json:"kind"`
	Target string                  `json:"target"`
}

type catalogReport struct {
	Operations   []catalogOperation `json:"operations"`
	ProtocolOnly []catalogProtocol  `json:"protocol_only"`
}

type catalogProtocol struct {
	Module   string `json:"module"`
	Listener string `json:"listener,omitempty"`
	Target   string `json:"target"`
	Reason   string `json:"reason"`
}

func catalogCommand(mc module.Context) *cli.Command {
	return &cli.Command{Name: "catalog", Help: "inspect the module catalog", Commands: []*cli.Command{{
		Name: "operations",
		Help: "list application operations and their bindings",
		Setup: func(a *cli.Args) cli.Handler {
			asJSON := a.Bool("json", cli.Opt{Help: "print JSON"})
			missing := a.Bool("missing-api", cli.Opt{Help: "print only the operator operations without a typed API binding"})
			unbound := a.Bool("unbound", cli.Opt{Help: "print only the mounted routes, commands, tools, jobs and services without a binding"})
			require := a.Bool("require-api", cli.Opt{Help: "exit 1 when an operator operation has no typed API binding"})
			return func(env *cli.Env) int {
				cat := mc.Catalog()
				if *unbound {
					found, err := catalogUnboundItems(env, cat)
					if err != nil {
						return cli.Fail(env, err)
					}
					if !*asJSON && len(found) > 0 {
						printUnbound(env, found)
						return cli.ExitOK
					}
					return printJSON(env, found)
				}
				ops := catalogOperations(cat, *missing || *require)
				code := cli.ExitOK
				if *require && len(ops) > 0 {
					code = cli.ExitError
				}
				if *missing {
					if !*asJSON && len(ops) > 0 {
						printOperations(env, ops)
						return code
					}
					if out := printJSON(env, ops); out != cli.ExitOK {
						return out
					}
					return code
				}
				report := catalogReport{Operations: ops, ProtocolOnly: catalogProtocols(cat)}
				if *asJSON {
					if out := printJSON(env, report); out != cli.ExitOK {
						return out
					}
				} else {
					printOperations(env, report.Operations)
				}
				return code
			}
		},
	}, {
		Name: "openapi",
		Help: "generate the OpenAPI document and API reach report",
		Setup: func(a *cli.Args) cli.Handler {
			output := a.String("output", "", cli.Opt{Help: "write openapi.json and reach.json into this directory"})
			check := a.String("check", "", cli.Opt{Help: "exit 1 when the files in this directory differ from the generated ones"})
			return func(env *cli.Env) int {
				files, err := apischema.Generate(mc.Catalog())
				if err != nil {
					return cli.Fail(env, err)
				}
				switch {
				case *check != "":
					for _, name := range []string{apischema.OpenAPIFile, apischema.ReachFile} {
						have, err := os.ReadFile(filepath.Join(*check, name))
						if err != nil || !bytes.Equal(have, files[name]) {
							_, _ = fmt.Fprintf(env.Stderr, "%s is out of date; regenerate it with: go run ./cmd/atrium catalog openapi --output %s\n", filepath.Join(*check, name), *check)
							return cli.ExitError
						}
					}
				case *output != "":
					if err := os.MkdirAll(*output, 0o755); err != nil {
						return cli.Fail(env, err)
					}
					for _, name := range []string{apischema.OpenAPIFile, apischema.ReachFile} {
						if err := os.WriteFile(filepath.Join(*output, name), files[name], 0o644); err != nil {
							return cli.Fail(env, err)
						}
					}
				default:
					_, _ = env.Stdout.Write(files[apischema.OpenAPIFile])
				}
				return cli.ExitOK
			}
		},
	}}}
}

func catalogOperations(cat *module.Catalog, missingAPI bool) []catalogOperation {
	bound := map[application.Name][]catalogBinding{}
	areas := map[application.Name]application.Area{}
	for _, b := range catalogBindings(cat) {
		for _, name := range b.Uses {
			bound[name] = append(bound[name], catalogBinding{Kind: b.Kind, Target: b.Target, Delegates: b.Delegates})
			if _, ok := areas[name]; !ok && b.API != nil {
				areas[name] = b.API.Area
			}
		}
	}
	out := []catalogOperation{}
	for _, op := range cat.Operations() {
		row := catalogOperation{
			Module: op.Module, Name: op.Name, Kind: op.Kind, Input: op.Input.String(), Output: op.Output.String(),
			Authority: op.Authority, Effect: op.Effect, Audit: op.Audit, Area: areas[op.Name], Bindings: bound[op.Name], APIRoutes: []string{},
		}
		slices.SortFunc(row.Bindings, func(a, b catalogBinding) int {
			return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Target, b.Target))
		})
		for _, b := range row.Bindings {
			if b.Kind == application.API {
				row.APIRoutes = append(row.APIRoutes, b.Target)
			}
		}
		if missingAPI && (row.Area != "" || !application.RequiresAPI(op.Authority)) {
			continue
		}
		out = append(out, row)
	}
	return out
}

func catalogUnboundItems(env *cli.Env, cat *module.Catalog) ([]catalogUnbound, error) {
	out := []catalogUnbound{}
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

func printJSON(env *cli.Env, v any) int {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return cli.Fail(env, err)
	}
	_, _ = fmt.Fprintln(env.Stdout, string(out))
	return cli.ExitOK
}

func printOperations(env *cli.Env, ops []catalogOperation) {
	w := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "MODULE\tNAME\tKIND\tAREA\tINPUT\tOUTPUT\tAUTHORITY\tEFFECT\tAUDIT\tBINDINGS")
	for _, op := range ops {
		authority := make([]string, len(op.Authority))
		for i, a := range op.Authority {
			authority[i] = string(a)
		}
		targets := make([]string, len(op.Bindings))
		for i, b := range op.Bindings {
			targets[i] = string(b.Kind) + ":" + b.Target
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%t\t%s\n", op.Module, op.Name, op.Kind, cmp.Or(op.Area, "-"), op.Input, op.Output,
			strings.Join(authority, ","), op.Effect, op.Audit, strings.Join(targets, " "))
	}
	_ = w.Flush()
}

func printUnbound(env *cli.Env, items []catalogUnbound) {
	w := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "MODULE\tKIND\tTARGET")
	for _, u := range items {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", u.Module, u.Kind, u.Target)
	}
	_ = w.Flush()
}
