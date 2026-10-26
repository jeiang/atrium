package media

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/jeiang/atrium/internal/integrations"
)

func init() {
	integrations.Register(integrations.Integration{
		Name: "qbittorrent", Label: "qBittorrent", Module: module, URLKey: KeyQbitURL,
		Test: func(ctx context.Context, c integrations.Conn) error {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/api/v2/app/version", nil)
			if err != nil {
				return err
			}
			resp, err := c.HTTP.Do(req)
			if err != nil {
				return err
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			switch {
			case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
				return fmt.Errorf("%w: qBittorrent answered %d", integrations.ErrAuth, resp.StatusCode)
			case resp.StatusCode != http.StatusOK:
				return fmt.Errorf("qBittorrent answered %d", resp.StatusCode)
			}
			return nil
		},
	})
}
