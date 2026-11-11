package imapclient

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/gmailauth"
)

func Opener(_ config.Config) mail.OpenFunc {
	return func(ctx context.Context, opts mail.OpenOptions) (mail.Client, error) {
		spec, known := config.From(ctx).Account(opts.Account)
		if !known {
			return nil, errors.New("missing credentials for " + opts.Account)
		}
		method, source, err := gmailauth.Resolve(ctx, opts.Q, opts.Account)
		if err != nil {
			return nil, err
		}
		_, password, ok := config.Credential(ctx, opts.Account)
		if method != config.AuthXOAUTH2 && !ok {
			return nil, errors.New("missing credentials for " + opts.Account)
		}
		bodyCap := mail.TuningMailBodyCapBytes
		var conn mail.Conn
		if method == config.AuthXOAUTH2 {
			c := NewXOAUTH2(spec.Host(), spec.Login, source.Token, source.Invalidate)
			if err := c.Connect(ctx); err != nil {
				c.Close()
				return nil, err
			}
			conn = c
		} else if conn, err = dial(ctx, spec, password); err != nil {
			return nil, err
		}
		if c, ok := conn.(interface{ setBodyCap(int) }); ok {
			c.setBodyCap(bodyCap)
		}
		return mail.Gated(conn, opts.Q), nil
	}
}
