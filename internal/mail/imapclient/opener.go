package imapclient

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/gmailauth"
	"github.com/jeiang/atrium/internal/mail/mailconn"
)

func Opener(_ config.Config) mail.OpenFunc {
	return func(ctx context.Context, opts mail.OpenOptions) (mail.Client, error) {
		spec, known := config.From(ctx).Account(opts.Account)
		if !known {
			return nil, errors.New("missing credentials for " + opts.Account)
		}
		lease, err := mailconn.Open(ctx, opts.Q, opts.Account)
		if err != nil {
			return nil, err
		}
		host := mailconn.IMAPHost(lease, spec)
		method, source, err := gmailauth.Resolve(ctx, opts.Q, opts.Account)
		if err != nil {
			return nil, err
		}
		var password string
		if method != config.AuthXOAUTH2 {
			password, err = mailconn.Password(lease, opts.Account)
			if err != nil {
				return nil, errors.New("missing credentials for " + opts.Account)
			}
		}
		bodyCap := mail.TuningMailBodyCapBytes
		var conn mail.Conn
		if method == config.AuthXOAUTH2 {
			c := NewXOAUTH2(host, spec.Login, source.Token, source.Invalidate)
			if err := c.Connect(ctx); err != nil {
				c.Close()
				return nil, err
			}
			conn = c
		} else if conn, err = dial(ctx, host, spec, password); err != nil {
			return nil, err
		}
		if c, ok := conn.(interface{ setBodyCap(int) }); ok {
			c.setBodyCap(bodyCap)
		}
		return mail.Gated(conn, opts.Q), nil
	}
}
