package notify

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

func PauseChanged(ctx context.Context, d *db.DB, env config.Env, paused bool) error {
	if !Enabled(ctx) {
		return nil
	}
	title := "atrium: mail writes resumed"
	if paused {
		title = "atrium: mail writes paused"
	}
	_, err := Send(ctx, d, Message{Channel: System, Priority: 4, Title: title, Body: "Changed by the operator."})
	return err
}

type Budget struct {
	SpentUSD              float64
	BudgetUSD             float64
	Share                 float64
	CommittedUSD          float64
	ReservedUSD           float64
	UnknownUSD            float64
	ExpiredUSD            float64
	InteractiveCeilingUSD float64
}

type BudgetStatus func(ctx context.Context, q db.Querier, now float64, loc *time.Location) (Budget, error)
