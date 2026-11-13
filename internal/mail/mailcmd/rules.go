package mailcmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/embed"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/report"
	"github.com/jeiang/atrium/internal/mail/rules"
)

func (o Options) RulesCommand() *cli.Command {
	return group("rules",
		o.leaf("import", func(a *cli.Args) body {
			file := a.Arg("file", cli.Opt{})
			return func(x *exec) error { return importRules(x, *file) }
		}),
		o.leaf("export", func(*cli.Args) body {
			return func(x *exec) error {
				read, err := x.opts.Application(x.Pass).RulesExport(x.Ctx, struct{}{})
				if err != nil {
					return err
				}
				b, err := json.MarshalIndent(read.Data, "", "  ")
				if err != nil {
					return err
				}
				x.Print(string(b) + "\n")
				return nil
			}
		}),
	)
}

func importRules(x *exec, file string) error {
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
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		return importJSONError(data, err)
	}
	done, err := x.opts.Application(x.Pass).RulesImport(x.Ctx, mailapp.RulesImportInput{Document: document})
	if err != nil {
		var ruleErr rules.RuleError
		if errors.As(err, &ruleErr) {
			return failure(cli.ExitUsage, err)
		}
		return err
	}
	x.Print(fmt.Sprintf("imported %d rules\n", done.Data.Count))
	return nil
}

func importJSONError(data []byte, err error) error {
	var syn *json.SyntaxError
	if !errors.As(err, &syn) {
		return err
	}
	i := 0
	for i < len(data) && (data[i] == ' ' || data[i] == '\t' || data[i] == '\n' || data[i] == '\r') {
		i++
	}
	line, col := 1, 1
	for j := 0; j < i; j++ {
		if data[j] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return fmt.Errorf("Expecting value: line %d column %d (char %d)", line, col, i) //nolint:staticcheck // the CLI contract keeps the py-compatible wording
}

func (o Options) ShadowReportCommand() *cli.Command {
	return o.leaf("shadow-report", func(a *cli.Args) body {
		since := a.String("since", "", cli.Opt{})
		return func(x *exec) error {
			var given *string
			if a.Has("since") {
				given = since
			}
			out, err := report.Build(x.Ctx, x.DB.Read, o.env(), given, clockTime(x.Now()))
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
		return func(x *exec) error {
			url, err := embed.EmbedURL(x.Ctx, x.DB.Read)
			if err != nil {
				return err
			}
			ctx, err := embed.WithModels(x.Ctx, x.DB.Read)
			if err != nil {
				return err
			}
			n, err := embed.Reindex(ctx, x.DB, url)
			if err != nil {
				return err
			}
			x.Print(fmt.Sprintf("embedded %d messages\n", n))
			return nil
		}
	})
}
