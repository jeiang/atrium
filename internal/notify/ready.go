package notify

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/ops"
)

const Integration = "ntfy"

func init() {
	ops.Ready(ops.Check{Name: "ntfy", Remote: true, Run: health})
	integrations.Register(integrations.Integration{
		Name: Integration, Label: "ntfy", Module: module, URLKey: KeyURL,
		Test: func(ctx context.Context, c integrations.Conn) (string, error) {
			return "", healthOf(ctx, c.HTTP, c.URL)
		},
	})
	integrations.SetNotifier(func(ctx context.Context, d *db.DB, env config.Env, title, body string) error {
		if on, err := Enabled(ctx, d.Read); err != nil || !on {
			return err
		}
		_, err := Send(ctx, d.Write, env, Message{Channel: System, Priority: 4, Title: title, Body: body})
		return err
	})
}

func healthOf(ctx context.Context, client *http.Client, base string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/v1/health", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: ntfy health answered %d", integrations.ErrAuth, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("ntfy health answered %d", resp.StatusCode)
	}
	return nil
}

func health(ctx context.Context, d *db.DB) error {
	if on, err := Enabled(ctx, d.Read); err != nil || !on {
		return err
	}
	base, err := text(ctx, d.Read, KeyURL)
	if err != nil || base == "" {
		return err
	}
	timeout, err := timeoutOf(ctx, d.Read)
	if err != nil {
		return err
	}
	client, err := newClient(ctx, d.Read, timeout)
	if err != nil {
		return err
	}
	return healthOf(ctx, client, base)
}
