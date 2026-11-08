package tracking

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jeiang/atrium/internal/integrations"
)

func init() {
	integrations.Register(integrations.Integration{
		Name: "ship24", Label: "Ship24", Module: Module,
		URLKey: KeyShip24URL, SecretKey: KeyShip24APIKey,
		Help: "API key from the Ship24 dashboard. The free plan registers 10 shipments a month.",
		Test: testShip24,
	})
}

func testShip24(ctx context.Context, c integrations.Conn) (string, error) {
	status, _, err := NewClient(c.HTTP, c.URL, c.Secret).do(ctx, http.MethodGet, "/trackers?limit=1", nil)
	if err != nil {
		return "", err
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", fmt.Errorf("%w: Ship24 refused the API key", integrations.ErrAuth)
	case http.StatusOK:
		return "API key works", nil
	}
	return "", fmt.Errorf("Ship24 answered %d", status)
}
