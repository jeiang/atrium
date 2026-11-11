package gmailauth

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
)

// Source adapts the shared google/default token source to the mail transport.
type Source struct{ lease *connections.Lease }

func (s *Source) Token(ctx context.Context) (string, error) {
	return s.lease.Token(ctx, connections.GoogleGmail)
}

func (s *Source) Invalidate() { s.lease.InvalidateToken(connections.GoogleGmail) }

func (s *Source) Close() { s.lease.Close() }

// Resolve returns the account's auth method and, for a Gmail XOAUTH2 account,
// the shared Google token source.
func Resolve(ctx context.Context, q db.Querier, account string) (config.AuthMethod, *Source, error) {
	spec, ok := config.From(ctx).Account(account)
	if !ok {
		return "", nil, errors.New("unknown mail account " + account)
	}
	if spec.Preset != config.Gmail || spec.AuthMethod != config.AuthXOAUTH2 {
		return config.AuthPassword, nil, nil
	}
	var (
		lease *connections.Lease
		err   error
	)
	if store := connections.DefaultFor(q); store != nil {
		lease, err = store.Open(ctx, connections.GoogleID)
	} else {
		lease, err = connections.OpenRead(ctx, q, config.From(ctx).Host(), connections.GoogleID)
	}
	if err != nil {
		var unknown connections.UnknownID
		if errors.As(err, &unknown) {
			return "", nil, errors.New("the google/default connection is not configured")
		}
		return "", nil, err
	}
	enabled := false
	for _, cap := range lease.Capabilities() {
		if cap.Name == connections.GoogleGmail && cap.Enabled {
			enabled = true
		}
	}
	if !enabled {
		lease.Close()
		return "", nil, errors.New("the Gmail capability is not enabled on google/default")
	}
	return config.AuthXOAUTH2, &Source{lease: lease}, nil
}
