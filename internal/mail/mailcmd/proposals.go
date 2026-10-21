package mailcmd

import (
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/rebalance"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/scans"
	"github.com/jeiang/atrium/internal/py"
)

func (o Options) ProposalsCommand() *cli.Command {
	return group("proposals",
		o.leaf("scan-all", func(*cli.Args) body {
			return func(x *Exec) error {
				url, err := mail.ChatURL(x.Ctx, x.DB.Read)
				if err != nil {
					return err
				}
				out, err := scans.ScanAll(x.Ctx, x.DB, x.Now(), rebalance.SuggestName(url))
				if err != nil {
					return err
				}
				x.JSON(out)
				return nil
			}
		}),
		o.leaf("list", func(a *cli.Args) body {
			status := a.String("status", "", cli.Opt{Choices: proposals.Statuses[:]})
			kind := a.String("kind", "", cli.Opt{})
			return func(x *Exec) error { return listProposals(x, *status, *kind) }
		}),
		o.leaf("approve", func(a *cli.Args) body {
			id := a.IntArg("id", cli.Opt{})
			selection := a.String("selection", "", cli.Opt{Help: "JSON selection document"})
			return func(x *Exec) error { return approve(x, int64(*id), *selection) }
		}),
		o.leaf("dismiss", func(a *cli.Args) body {
			id := a.IntArg("id", cli.Opt{})
			forever := a.Bool("forever", cli.Opt{})
			return func(x *Exec) error {
				if err := proposals.Dismiss(x.Ctx, x.DB.Write, int64(*id), *forever, x.Now()); err != nil {
					return err
				}
				x.JSON(py.Dict{{Key: "id", Value: *id}, {Key: "dismissed", Value: true}})
				return nil
			}
		}),
		o.leaf("undo", func(a *cli.Args) body {
			id := a.IntArg("id", cli.Opt{})
			return func(x *Exec) error {
				result, err := x.Pipeline().Undo(x.Ctx, int64(*id))
				if err != nil {
					return err
				}
				x.JSON(result.Dict())
				return nil
			}
		}),
	)
}

func (x *Exec) Pipeline() *proposals.Pipeline {
	return &proposals.Pipeline{DB: x.DB, Registry: x.Registry(), Now: x.opts.Now}
}

func listProposals(x *Exec, status, kind string) error {
	list, err := proposals.List(x.Ctx, x.DB.Read, status, kind)
	if err != nil {
		return err
	}
	out := make([]any, len(list))
	for i, p := range list {
		out[i] = py.Dict{
			{Key: "id", Value: p.ID}, {Key: "kind", Value: p.Kind}, {Key: "account", Value: nullable(p.Account)},
			{Key: "status", Value: p.Status}, {Key: "title", Value: p.Title}, {Key: "undoable", Value: p.Undoable},
		}
	}
	x.JSON(out)
	return nil
}

func approve(x *Exec, id int64, selection string) error {
	var chosen any
	if selection != "" {
		parsed, err := py.JSONLoads([]byte(selection))
		if err != nil {
			return err
		}
		chosen = parsed
	}
	approved, err := x.Pipeline().ApproveAndApply(x.Ctx, id, chosen)
	if err != nil {
		return err
	}
	x.JSON(approved.Dict())
	if approved.Status == proposals.Failed {
		return x.Ctx.Err()
	}
	return nil
}
