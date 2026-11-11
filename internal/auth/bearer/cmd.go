package bearer

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const actor = "operator"

func Command(env config.Env) *cli.Command {
	s := &cmdService{env: env}
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

type cmdService struct{ env config.Env }

func (s *cmdService) issue(a *cli.Args) cli.Handler {
	name := a.String("name", "", cli.Opt{Required: true, Help: "label for the token"})
	expires := a.String("expires-at", "", cli.Opt{Help: "RFC 3339 expiry; the default is no expiry"})
	return func(e *cli.Env) int {
		var at *time.Time
		if raw := *expires; raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return e.UsageError("--expires-at must be an RFC 3339 timestamp")
			}
			at = &parsed
		}
		d, code := s.open(e)
		if code != cli.ExitOK {
			return code
		}
		defer func() { _ = d.Close() }()
		ctx := audit.With(e.Ctx, actor, audit.CLI)
		issued, err := New(d, nil).Issue(ctx, *name, at)
		if errors.Is(err, ErrName) || errors.Is(err, ErrExpiry) {
			return e.UsageError("%v", err)
		}
		if err != nil {
			return cli.Fail(e, err)
		}
		_, _ = fmt.Fprintln(e.Stdout, issued.Secret)
		_, _ = fmt.Fprintf(e.Stderr, "issued token %d (%s) for %q\n", issued.Token.ID, issued.Token.Kind, issued.Token.Name)
		if err := record(ctx, d, "auth_token.issue", issued.Token); err != nil {
			_, _ = fmt.Fprintf(e.Stderr, "error: %v\n", err)
			return cli.ExitError
		}
		return cli.ExitOK
	}
}

func (s *cmdService) list(*cli.Args) cli.Handler {
	return func(e *cli.Env) int {
		d, code := s.open(e)
		if code != cli.ExitOK {
			return code
		}
		defer func() { _ = d.Close() }()
		tokens, err := New(d, nil).List(e.Ctx)
		if err != nil {
			return cli.Fail(e, err)
		}
		_, _ = fmt.Fprintln(e.Stdout, "ID\tNAME\tKIND\tCREATED\tEXPIRES\tLAST USED\tREVOKED")
		for _, tok := range tokens {
			_, _ = fmt.Fprintf(e.Stdout, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
				tok.ID, tok.Name, tok.Kind, stamp(tok.CreatedAt), stampOpt(tok.ExpiresAt), stampOpt(tok.LastUsedAt), stampOpt(tok.RevokedAt))
		}
		return cli.ExitOK
	}
}

func (s *cmdService) revoke(a *cli.Args) cli.Handler {
	id := a.Arg("id", cli.Opt{Help: "token id from the list"})
	return func(e *cli.Env) int {
		n, err := strconv.ParseInt(strings.TrimSpace(*id), 10, 64)
		if err != nil || n < 1 {
			return e.UsageError("id must be a positive integer")
		}
		d, code := s.open(e)
		if code != cli.ExitOK {
			return code
		}
		defer func() { _ = d.Close() }()
		ctx := audit.With(e.Ctx, actor, audit.CLI)
		service := New(d, nil)
		tokens, err := service.List(ctx)
		if err != nil {
			return cli.Fail(e, err)
		}
		var target Token
		for _, tok := range tokens {
			if tok.ID == n {
				target = tok
			}
		}
		if err := service.Revoke(ctx, n); errors.Is(err, ErrNotFound) {
			_, _ = fmt.Fprintf(e.Stderr, "error: no token %d\n", n)
			return cli.ExitError
		} else if err != nil {
			return cli.Fail(e, err)
		}
		_, _ = fmt.Fprintf(e.Stdout, "revoked %d\n", n)
		if target.ID != 0 {
			if err := record(ctx, d, "auth_token.revoke", target); err != nil {
				_, _ = fmt.Fprintf(e.Stderr, "error: %v\n", err)
				return cli.ExitError
			}
		}
		return cli.ExitOK
	}
}

func (s *cmdService) open(e *cli.Env) (*db.DB, int) {
	d, _, err := config.OpenStore(e.Ctx, s.env)
	if err != nil {
		return nil, cli.Fail(e, err)
	}
	return d, cli.ExitOK
}

func record(ctx context.Context, d *db.DB, key string, tok Token) error {
	value := fmt.Sprintf("id=%d name=%s kind=%s", tok.ID, tok.Name, tok.Kind)
	return audit.Record(ctx, d.Write, audit.Entry{Key: key, New: &value})
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
