package notify

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ops"
)

func init() {
	ops.Ready(ops.Check{Name: "ntfy", Remote: true, Run: health})
}

func health(ctx context.Context, d *db.DB) error {
	if on, err := Enabled(ctx, d.Read); err != nil || !on {
		return err
	}
	base, err := text(ctx, d.Read, KeyURL)
	if err != nil || base == "" {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/v1/health", nil)
	if err != nil {
		return err
	}
	timeout, err := timeoutOf(ctx, d.Read)
	if err != nil {
		return err
	}
	resp, err := newClient(timeout).Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ntfy health answered %d", resp.StatusCode)
	}
	return nil
}
