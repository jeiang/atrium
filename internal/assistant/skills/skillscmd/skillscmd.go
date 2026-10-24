package skillscmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant/skills"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

func Command() *cli.Command {
	return &cli.Command{Name: "skills", Commands: []*cli.Command{exportCommand(config.OS), importCommand(config.OS)}}
}

func open(env *cli.Env, cfgEnv config.Env) (*db.DB, error) {
	cfg, err := config.Load(cfgEnv)
	if err != nil {
		return nil, err
	}
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	if err := config.Migrate(env.Ctx, d, cfg); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

func fail(env *cli.Env, err error) int {
	var refused skills.Error
	if errors.As(err, &refused) {
		_, _ = fmt.Fprintf(env.Stderr, "error: %s\n", refused)
		return cli.ExitError
	}
	return cli.Fail(env, err)
}

func exportCommand(cfgEnv config.Env) *cli.Command {
	return &cli.Command{
		Name: "export",
		Setup: func(args *cli.Args) cli.Handler {
			names := args.Repeated("name", cli.Opt{Help: "skill to export; repeat for several, default all"})
			dir := args.Arg("dir", cli.Opt{Help: "directory that receives one <name>/SKILL.md per skill"})
			return func(env *cli.Env) int {
				d, err := open(env, cfgEnv)
				if err != nil {
					return fail(env, err)
				}
				defer func() { _ = d.Close() }()
				list, err := skills.Export(env.Ctx, d.Read, *names)
				if err != nil {
					return fail(env, err)
				}
				for _, s := range list {
					folder := filepath.Join(*dir, s.Name)
					if err := os.MkdirAll(folder, 0o755); err != nil {
						return fail(env, err)
					}
					if err := os.WriteFile(filepath.Join(folder, skills.FileName), []byte(skills.Render(s)), 0o644); err != nil {
						return fail(env, err)
					}
					_, _ = fmt.Fprintln(env.Stdout, s.Name)
				}
				return cli.ExitOK
			}
		},
	}
}

func importCommand(cfgEnv config.Env) *cli.Command {
	return &cli.Command{
		Name: "import",
		Setup: func(args *cli.Args) cli.Handler {
			source := args.Arg("path", cli.Opt{Help: "directory of skill folders, a skill folder, a SKILL.md file or a .zip archive"})
			return func(env *cli.Env) int {
				d, err := open(env, cfgEnv)
				if err != nil {
					return fail(env, err)
				}
				defer func() { _ = d.Close() }()
				limits, err := skills.ReadLimits(env.Ctx, d.Read)
				if err != nil {
					return fail(env, err)
				}
				items, err := collect(*source, limits)
				if err != nil {
					return fail(env, err)
				}
				done, err := skills.Import(env.Ctx, d, items, float64(time.Now().UnixMicro())/1e6)
				if err != nil {
					return fail(env, err)
				}
				for _, name := range done.Created {
					_, _ = fmt.Fprintln(env.Stdout, "created "+name)
				}
				for _, name := range done.Updated {
					_, _ = fmt.Fprintln(env.Stdout, "updated "+name)
				}
				for _, name := range done.Unchanged {
					_, _ = fmt.Fprintln(env.Stdout, "unchanged "+name)
				}
				return cli.ExitOK
			}
		},
	}
}

func collect(source string, limits skills.Limits) ([]skills.Item, error) {
	info, err := os.Stat(source)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		items, err := skills.Collect(os.DirFS(source), limits)
		for i := range items {
			if items[i].Name == "" && filepath.Dir(items[i].Source) == "." {
				items[i].Name = filepath.Base(absolute(source))
			}
		}
		return items, err
	}
	if info.Size() > skills.MaxImportBytes {
		return nil, skills.Error(source + ": too large")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(filepath.Ext(source), ".zip") {
		return skills.CollectZip(data, limits)
	}
	items, err := skills.CollectFile(source, data, limits)
	for i := range items {
		if items[i].Name == "" {
			items[i].Name = filepath.Base(filepath.Dir(absolute(source)))
		}
	}
	return items, err
}

func absolute(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}
