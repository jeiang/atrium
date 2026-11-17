package imapclient

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/apps/mail"
	"github.com/jeiang/atrium/internal/apps/mail/gmailauth"
	"github.com/jeiang/atrium/internal/apps/mail/mailconn"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/runtime"
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
		if h, held := lease.Held(ctx, mailconn.CapabilityIMAP); held && runtime.IsUnattended(ctx) {
			return nil, h.Skipped(lease.ID(), mailconn.CapabilityIMAP)
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
		dialed := false
		client, err := mail.Gated(ctx, opts, func() (mail.Conn, error) {
			dialed = true
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
				c.setBodyCap(mail.TuningMailBodyCapBytes)
			}
			return conn, nil
		})
		if dialed && ctx.Err() == nil {
			outcome := mailconn.IMAPOutcome(err)
			if errors.Is(err, mail.ErrLoginRefused) {
				err = lease.Refuse(ctx, outcome, err)
			}
			lease.Report(outcome)
		}
		return client, err
	}
}
