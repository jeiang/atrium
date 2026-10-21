package imapclient

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
)

func Opener(cfg config.Config) mail.OpenFunc {
	return func(ctx context.Context, account string) (mail.Client, error) {
		user, password, ok := cfg.Credential(account)
		spec, known := config.Account(account)
		if !ok || !known {
			return nil, errors.New("missing credentials for " + account)
		}
		client := New(spec.Host, user, password)
		if err := client.Connect(ctx); err != nil {
			client.Close()
			return nil, err
		}
		return client, nil
	}
}
