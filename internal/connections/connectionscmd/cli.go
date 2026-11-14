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
			{Name: "test", Help: "test one connection or every configured connection and save the outcome", Setup: s.test},
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
			_, _ = fmt.Fprintf(e.Stdout, "%s\t%s\t%s\t%s\t%s\n", v.ID, v.Kind, credentialSummary(v), v.State, reasonOr(v.Reason, "-"))
		}
		return cli.ExitOK
	}
}

func (s *service) test(a *cli.Args) cli.Handler {
	id := a.Arg("id", cli.Opt{Help: "connection id"})
	all := a.Bool("all", cli.Opt{Help: "test every configured connection"})
	jsonOut := a.Bool("json", cli.Opt{Help: "print JSON"})
	return func(e *cli.Env) int {
		if (*id == "") == !*all {
			return e.UsageError("give a connection id or --all")
		}
		svc, closeDB, err := s.open(e.Ctx)
		if err != nil {
			return cli.Fail(e, err)
		}
		defer closeDB()
		change, err := svc.Test(e.Ctx, connectionsapp.TestInput{ID: connectionsapp.ID(*id), All: *all})
		if err != nil {
			return cli.Fail(e, err)
		}
		views := change.Data
		if *jsonOut {
			if code := writeJSON(e, views); code != cli.ExitOK {
				return code
			}
		} else {
			for _, v := range views {
				_, _ = fmt.Fprintf(e.Stdout, "%s\t%s\t%s\n", v.ID, v.State, reasonOr(v.Reason, "-"))
			}
		}
		for _, v := range views {
			if v.State == "failing" || v.State == "needs_reauth" {
				return cli.ExitError
			}
		}
		if !*all && len(views) == 1 && (views[0].State == "unconfigured" || views[0].State == "disabled") {
			return cli.ExitError
		}
		return cli.ExitOK
	}
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
