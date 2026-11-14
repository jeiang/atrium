package imapclient

import (
	"context"
	"sync/atomic"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
)

type Dialer = func(ctx context.Context, a config.AccountSpec, password string) (mail.Conn, error)

var dialer atomic.Pointer[Dialer]

func UseDialer(d Dialer) (restore func()) {
	previous := dialer.Swap(&d)
	return func() { dialer.Store(previous) }
}

func dial(ctx context.Context, host string, a config.AccountSpec, password string) (mail.Conn, error) {
	if d := dialer.Load(); d != nil {
		return (*d)(ctx, a, password)
	}
	c := New(host, a.Login, password)
	if err := c.Connect(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Check authenticates and reads the mailbox list without changing anything.
func Check(ctx context.Context, host string, a config.AccountSpec, password string) error {
	c, err := dial(ctx, host, a, password)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.ListMailboxes(ctx)
	return err
}

// CheckXOAUTH2 authenticates with a bearer token and reads the mailbox list.
func CheckXOAUTH2(ctx context.Context, host string, a config.AccountSpec, token func(context.Context) (string, error), invalidate func()) error {
	if d := dialer.Load(); d != nil {
		c, err := (*d)(ctx, a, "")
		if err != nil {
			return err
		}
		defer c.Close()
		_, err = c.ListMailboxes(ctx)
		return err
	}
	c := NewXOAUTH2(host, a.Login, token, invalidate)
	if err := c.Connect(ctx); err != nil {
		c.Close()
		return err
	}
	defer c.Close()
	_, err := c.ListMailboxes(ctx)
	return err
}
