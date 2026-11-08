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

func dial(ctx context.Context, a config.AccountSpec, password string) (mail.Conn, error) {
	if d := dialer.Load(); d != nil {
		return (*d)(ctx, a, password)
	}
	c := New(a.Host(), a.Login, password)
	if err := c.Connect(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func Login(ctx context.Context, a config.AccountSpec, password string) error {
	c, err := dial(ctx, a, password)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.ListMailboxes(ctx)
	return err
}
