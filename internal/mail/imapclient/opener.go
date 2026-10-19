package imapclient

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
)

// Opener is run.open_client: the connected, write-gated client of an account from the configured credentials. It
// is assignable to the mailops, sync and drafts Opener types.
func Opener(cfg config.Config, env config.Env) func(ctx context.Context, account string) (mail.Client, error) {
	return func(ctx context.Context, account string) (mail.Client, error) {
		user, password, ok := cfg.Credential(account)
		spec, known := config.Account(account)
		if !ok || !known {
			return nil, errors.New("missing credentials for " + account)
		}
		client := New(spec.Host, user, password, env)
		if err := client.Connect(ctx); err != nil {
			client.Close()
			return nil, err
		}
		return client, nil
	}
}
