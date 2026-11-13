package bearercmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	authapp "github.com/jeiang/atrium/internal/auth/application"
	"github.com/jeiang/atrium/internal/cli"
)

func Command(open authapp.Open) *cli.Command {
	s := &service{open: open}
	return &cli.Command{
		Name: "auth",
		Commands: []*cli.Command{
			{Name: "token", Help: "issue, list and revoke public API bearer tokens", Commands: []*cli.Command{
				{Name: "issue", Help: "issue a local API token and print its secret once", Setup: s.issue},
				{Name: "list", Help: "list API tokens without their secrets", Setup: s.list},
				{Name: "revoke", Help: "revoke an API token by id", Setup: s.revoke},
			}},
		},
	}
}

type service struct{ open authapp.Open }

func (s *service) issue(a *cli.Args) cli.Handler {
	name := a.String("name", "", cli.Opt{Required: true, Help: "label for the token"})
	expires := a.String("expires-at", "", cli.Opt{Help: "RFC 3339 expiry; the default is no expiry"})
	return func(e *cli.Env) int {
		in := authapp.BearerIssueInput{Name: *name}
		if raw := *expires; raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return e.UsageError("--expires-at must be an RFC 3339 timestamp")
			}
			in.ExpiresAt = &parsed
		}
		svc, closeDB, err := s.open(e.Ctx)
		if err != nil {
			return cli.Fail(e, err)
		}
		defer closeDB()
		issued, err := svc.BearerIssue(e.Ctx, in)
		if err != nil {
			return cli.Fail(e, err)
		}
		tok := issued.Data.Token
		_, _ = fmt.Fprintln(e.Stdout, issued.Data.Secret)
		_, _ = fmt.Fprintf(e.Stderr, "issued token %d (%s) for %q\n", tok.ID, tok.Kind, tok.Name)
		return cli.ExitOK
	}
}

func (s *service) list(*cli.Args) cli.Handler {
	return func(e *cli.Env) int {
		svc, closeDB, err := s.open(e.Ctx)
		if err != nil {
			return cli.Fail(e, err)
		}
		defer closeDB()
		tokens, err := svc.BearerList(e.Ctx)
		if err != nil {
			return cli.Fail(e, err)
		}
		_, _ = fmt.Fprintln(e.Stdout, "ID\tNAME\tKIND\tCREATED\tEXPIRES\tLAST USED\tREVOKED")
		for _, tok := range tokens.Data {
			_, _ = fmt.Fprintf(e.Stdout, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
				tok.ID, tok.Name, tok.Kind, stamp(tok.CreatedAt), stampOpt(tok.ExpiresAt), stampOpt(tok.LastUsedAt), stampOpt(tok.RevokedAt))
		}
		return cli.ExitOK
	}
}

func (s *service) revoke(a *cli.Args) cli.Handler {
	id := a.Arg("id", cli.Opt{Help: "token id from the list"})
	return func(e *cli.Env) int {
		n, err := strconv.ParseInt(strings.TrimSpace(*id), 10, 64)
		if err != nil || n < 1 {
			return e.UsageError("id must be a positive integer")
		}
		svc, closeDB, err := s.open(e.Ctx)
		if err != nil {
			return cli.Fail(e, err)
		}
		defer closeDB()
		if _, err := svc.BearerRevoke(e.Ctx, authapp.BearerRevokeInput{ID: n}); err != nil {
			return cli.Fail(e, err)
		}
		_, _ = fmt.Fprintf(e.Stdout, "revoked %d\n", n)
		return cli.ExitOK
	}
}

func stamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func stampOpt(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return stamp(*t)
}
