package settingscmd

import (
	"encoding/json"
	"io"

	"github.com/jeiang/atrium/internal/cli"
	settingsapp "github.com/jeiang/atrium/internal/settings/application"
)

func Command(open settingsapp.Open) *cli.Command {
	c := &command{open: open}
	return &cli.Command{
		Name: "settings",
		Commands: []*cli.Command{
			{Name: "list", Help: "list every setting with its effective value, source and class", Setup: c.list},
			{Name: "get", Help: "print one setting", Setup: c.get},
			{Name: "set", Help: "store an instance-level value", Setup: c.set},
			{Name: "unset", Help: "remove the stored instance-level value", Setup: c.unset},
		},
	}
}

type command struct{ open settingsapp.Open }

func (c *command) list(*cli.Args) cli.Handler {
	return func(e *cli.Env) int {
		return c.with(e, func(s *settingsapp.Service) error {
			read, err := s.Read(e.Ctx, settingsapp.ReadInput{All: true})
			if err != nil {
				return err
			}
			rows := read.Data.Rows
			if rows == nil {
				rows = []settingsapp.Setting{}
			}
			writeRows(e.Stdout, rows)
			return nil
		})
	}
}

func (c *command) get(a *cli.Args) cli.Handler {
	key := a.Arg("key", cli.Opt{})
	return func(e *cli.Env) int {
		return c.with(e, func(s *settingsapp.Service) error { return show(e, s, *key) })
	}
}

func (c *command) set(a *cli.Args) cli.Handler {
	key := a.Arg("key", cli.Opt{})
	value := a.Arg("value", cli.Opt{})
	return func(e *cli.Env) int {
		return c.with(e, func(s *settingsapp.Service) error {
			if _, err := s.Set(e.Ctx, settingsapp.SetInput{Values: []settingsapp.Value{{Key: *key, Value: *value}}}); err != nil {
				return err
			}
			return show(e, s, *key)
		})
	}
}

func (c *command) unset(a *cli.Args) cli.Handler {
	key := a.Arg("key", cli.Opt{})
	return func(e *cli.Env) int {
		return c.with(e, func(s *settingsapp.Service) error {
			if _, err := s.Unset(e.Ctx, settingsapp.UnsetInput{Key: *key}); err != nil {
				return err
			}
			return show(e, s, *key)
		})
	}
}

func show(e *cli.Env, s *settingsapp.Service, key string) error {
	read, err := s.Read(e.Ctx, settingsapp.ReadInput{Keys: []string{key}})
	if err != nil {
		return err
	}
	writeRows(e.Stdout, read.Data.Rows[0])
	return nil
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

func writeRows(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
