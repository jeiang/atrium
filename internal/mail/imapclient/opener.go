package imapclient

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/accounts"
	"github.com/jeiang/atrium/internal/mail/gmailauth"
)

func Opener(cfg config.Config) mail.OpenFunc {
	return func(ctx context.Context, opts mail.OpenOptions) (mail.Client, error) {
		spec, known := config.Account(opts.Account)
		if !known {
			return nil, errors.New("missing credentials for " + opts.Account)
		}
		method, source, err := gmailauth.Resolve(ctx, opts.Q, opts.Account)
		if err != nil {
			return nil, err
		}
		_, password, ok := cfg.Credential(opts.Account)
		if method != accounts.XOAUTH2 && !ok {
			return nil, errors.New("missing credentials for " + opts.Account)
		}
		bodyCap := config.BodyCapBytes
		if opts.Q != nil {
			r := endpoint.Read(ctx, opts.Q)
			bodyCap = r.Int(mail.KeyBodyCapBytes)
			if err := r.Err(); err != nil {
				return nil, err
			}
		}
		var conn mail.Conn
		if method == accounts.XOAUTH2 {
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
