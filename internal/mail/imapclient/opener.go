package imapclient

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
)

func Opener(cfg config.Config) mail.OpenFunc {
	return func(ctx context.Context, opts mail.OpenOptions) (mail.Client, error) {
		_, password, ok := cfg.Credential(opts.Account)
		spec, known := config.Account(opts.Account)
		if !ok || !known {
			return nil, errors.New("missing credentials for " + opts.Account)
		}
		conn, err := dial(ctx, spec, password)
		if err != nil {
			return nil, err
		}
		return mail.Gated(conn, opts.Q), nil
	}
}
