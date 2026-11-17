package connectionscmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/cli"
	connectionsapp "github.com/jeiang/atrium/internal/connections/application"
)

func Command(open connectionsapp.Open) *cli.Command {
	s := &service{open: open}
	return &cli.Command{
		Name: "connections",
		Commands: []*cli.Command{
			{Name: "list", Help: "list connections with their capabilities, credential presence and health", Setup: s.list},
			{Name: "test", Help: "test one connection, with its side effects, or check every configured connection without them, and save the outcome", Setup: s.test},
			{Name: "check", Help: "check configured connections without test side effects and save the outcome", Setup: s.check},
		},
	}
}

type service struct{ open connectionsapp.Open }

func (s *service) list(a *cli.Args) cli.Handler {
	jsonOut := a.Bool("json", cli.Opt{Help: "print JSON"})
	return func(e *cli.Env) int {
		svc, closeDB, err := s.open(e.Ctx)
		if err != nil {
			return cli.Fail(e, err)
		}
		defer closeDB()
		read, err := svc.List(e.Ctx, connectionsapp.ListInput{})
		if err != nil {
			return cli.Fail(e, err)
		}
		views := read.Data
		if *jsonOut {
			return writeJSON(e, views)
		}
		for _, v := range views {
			_, _ = fmt.Fprintf(e.Stdout, "%s\t%s\t%s\t%s\t%s\n", v.ID, v.Kind, credentialSummary(v), v.Status(), reasonOr(v.Reason, "-"))
		}
		return cli.ExitOK
	}
}

func (s *service) test(a *cli.Args) cli.Handler {
	id := a.OptionalArg("id", cli.Opt{Help: "connection id"})
	all := a.Bool("all", cli.Opt{Help: "check every configured connection without test side effects instead of testing one"})
	trace := a.String("trace", "", cli.Opt{Help: "print every request and response of one capability's check, with ids, user names and addresses masked, and save nothing"})
	jsonOut := a.Bool("json", cli.Opt{Help: "print JSON"})
	return func(e *cli.Env) int {
		if (*id == "") == !*all {
			return e.UsageError("give a connection id or --all")
		}
		if *trace != "" && (*all || *jsonOut) {
			return e.UsageError("--trace takes one connection id and prints text")
		}
		svc, closeDB, err := s.open(e.Ctx)
		if err != nil {
			return cli.Fail(e, err)
		}
		defer closeDB()
		if *trace != "" {
			result, err := svc.Trace(e.Ctx, connectionsapp.TraceInput{ID: connectionsapp.ID(*id), Capability: *trace, Out: e.Stdout})
			if err != nil {
				return cli.Fail(e, err)
			}
			_, _ = fmt.Fprintf(e.Stdout, "result: %s %s %s\n", result.Capability, result.State, reasonOr(result.Reason, "-"))
			if result.State == "failing" || result.State == "needs_reauth" {
				return cli.ExitError
			}
			return cli.ExitOK
		}
		change, err := svc.Test(e.Ctx, connectionsapp.TestInput{ID: connectionsapp.ID(*id), All: *all})
		if err != nil {
			return cli.Fail(e, err)
		}
		views := change.Data
		if code := report(e, views, *jsonOut); code != cli.ExitOK {
			return code
		}
		if !*all && len(views) == 1 && (views[0].State == "unconfigured" || views[0].State == "disabled") {
			return cli.ExitError
		}
		return cli.ExitOK
	}
}

func (s *service) check(a *cli.Args) cli.Handler {
	changed := a.Bool("changed", cli.Opt{Help: "check only connections whose configuration changed since their last check"})
	jsonOut := a.Bool("json", cli.Opt{Help: "print JSON"})
	return func(e *cli.Env) int {
		svc, closeDB, err := s.open(e.Ctx)
		if err != nil {
			return cli.Fail(e, err)
		}
		defer closeDB()
		change, err := svc.Check(e.Ctx, connectionsapp.CheckInput{Changed: *changed})
		if err != nil {
			return cli.Fail(e, err)
		}
		if len(change.Data) == 0 && !*jsonOut {
			_, _ = fmt.Fprintln(e.Stdout, "no connection to check")
			return cli.ExitOK
		}
		return report(e, change.Data, *jsonOut)
	}
}

func report(e *cli.Env, views []connectionsapp.View, jsonOut bool) int {
	if jsonOut {
		if code := writeJSON(e, views); code != cli.ExitOK {
			return code
		}
	} else {
		for _, v := range views {
			_, _ = fmt.Fprintf(e.Stdout, "%s\t%s\t%s\n", v.ID, v.Status(), reasonOr(v.Reason, "-"))
		}
	}
	for _, v := range views {
		if v.State == "failing" || v.State == "needs_reauth" {
			return cli.ExitError
		}
	}
	return cli.ExitOK
}

func writeJSON(e *cli.Env, v any) int {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return cli.Fail(e, err)
	}
	_, _ = fmt.Fprintln(e.Stdout, string(raw))
	return cli.ExitOK
}

func credentialSummary(v connectionsapp.View) string {
	var parts []string
	for _, c := range v.Credentials {
		state := "missing"
		if c.Present {
			state = c.Source
		}
		parts = append(parts, c.Slot+"="+state)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

func reasonOr(reason, fallback string) string {
	if strings.TrimSpace(reason) == "" {
		return fallback
	}
	return reason
}
