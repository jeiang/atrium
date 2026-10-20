package mailcmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/mail/report"
	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/py"
)

func (o Options) RulesCommand() *cli.Command {
	return group("rules",
		o.leaf("import", func(a *cli.Args) body {
			file := a.Arg("file", cli.Opt{})
			return func(x *Exec) error { return importRules(x, *file) }
		}),
		o.leaf("export", func(*cli.Args) body {
			return func(x *Exec) error {
				out, err := rules.ExportRules(x.Ctx, x.DB.Read)
				if err != nil {
					return err
				}
				x.Print(py.JSONDumps(out, py.JSONOpts{Indent: 2}) + "\n")
				return nil
			}
		}),
	)
}

func importRules(x *Exec, file string) error {
	var data []byte
	var err error
	if file == "-" {
		data, err = io.ReadAll(x.Stdin)
	} else if data, err = os.ReadFile(file); err != nil {
		err = openError(file, err)
	}
	if err != nil {
		return err
	}
	document, err := py.JSONLoads(data)
	if err != nil {
		return err
	}
	count, err := rules.ImportRules(x.Ctx, x.DB, document)
	if err != nil {
		var ruleErr rules.RuleError
		if errors.As(err, &ruleErr) {
			return failure(cli.ExitUsage, err)
		}
		return err
	}
	x.Print(fmt.Sprintf("imported %d rules\n", count))
	return nil
}

func (o Options) ShadowReportCommand() *cli.Command {
	return o.leaf("shadow-report", func(a *cli.Args) body {
		since := a.String("since", "", cli.Opt{})
		return func(x *Exec) error {
			var given *string
			if a.Has("since") {
				given = since
			}
			out, err := report.Build(x.Ctx, x.DB.Read, config.OS, given, clockTime(x.Now()))
			if err != nil {
				return err
			}
			x.Print(out)
			return nil
		}
	})
}

func clockTime(ts float64) time.Time {
	return time.Unix(0, int64(ts*1e9))
}

func (o Options) ReindexCommand() *cli.Command {
	return o.leaf("reindex", func(*cli.Args) body {
		return func(x *Exec) error {
			n, err := embed.Reindex(x.Ctx, x.DB, x.Cfg.EmbedURL)
			if err != nil {
				return err
			}
			x.Print(fmt.Sprintf("embedded %d messages\n", n))
			return nil
		}
	})
}
