package secretscmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/jeiang/atrium/internal/cli"
	settingsapp "github.com/jeiang/atrium/internal/settings/application"
)

const maxValue = 1 << 20

func Command(open settingsapp.Open) *cli.Command {
	c := &command{open: open}
	return &cli.Command{
		Name: "secrets",
		Commands: []*cli.Command{
			{Name: "set", Help: "store a secret read from standard input", Setup: c.set},
			{Name: "delete", Help: "remove a stored secret", Setup: c.delete},
			{Name: "status", Help: "show whether each secret is set, unset or pinned by config", Setup: c.status},
		},
	}
}

type command struct{ open settingsapp.Open }

func (c *command) set(a *cli.Args) cli.Handler {
	name := a.Arg("name", cli.Opt{})
	a.RejectExtras("the value must come from standard input, not the command line")
	return func(e *cli.Env) int {
		raw, err := io.ReadAll(io.LimitReader(e.Stdin, maxValue+1))
		if err != nil {
			return cli.Fail(e, err)
		}
		if len(raw) > maxValue {
			return e.UsageError("the secret on standard input is larger than %d bytes", maxValue)
		}
		value := strings.TrimRight(string(raw), "\r\n")
		return c.with(e, func(s *settingsapp.Service) error {
			change, err := s.SetSecret(e.Ctx, settingsapp.SecretSetInput{Name: *name, Value: value})
			if err != nil {
				return err
			}
			report(e, change.Data)
			return nil
		})
	}
}

func (c *command) delete(a *cli.Args) cli.Handler {
	name := a.Arg("name", cli.Opt{})
	return func(e *cli.Env) int {
		return c.with(e, func(s *settingsapp.Service) error {
			change, err := s.DeleteSecret(e.Ctx, settingsapp.SecretDeleteInput{Name: *name})
			if err != nil {
				return err
			}
			report(e, change.Data)
			return nil
		})
	}
}

func (c *command) status(*cli.Args) cli.Handler {
	return func(e *cli.Env) int {
		return c.with(e, func(s *settingsapp.Service) error {
			read, err := s.SecretStatus(e.Ctx, settingsapp.SecretStatusInput{})
			if err != nil {
				return err
			}
			for _, state := range read.Data {
				report(e, state)
			}
			return nil
		})
	}
}

func report(e *cli.Env, state settingsapp.SecretState) {
	_, _ = fmt.Fprintf(e.Stdout, "%s: %s\n", state.Name, state.Status)
}

func (c *command) with(e *cli.Env, run func(*settingsapp.Service) error) int {
	s, closeDB, err := c.open(e.Ctx)
	if err != nil {
		return cli.Fail(e, err)
	}
	defer closeDB()
	if err := run(s); err != nil {
		return cli.Fail(e, err)
	}
	return cli.ExitOK
}
