package inference

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/db"
)

const (
	classificationRoomPromptTokens = 8000
	classificationRoomOutputTokens = 2000
)

type ClassificationDeferral struct {
	MonthStart   float64 `json:"month_start"`
	FirstAt      float64 `json:"first_at"`
	LastAt       float64 `json:"last_at"`
	LastCaller   string  `json:"last_caller"`
	LastItemKey  string  `json:"last_item_key"`
	BudgetUSD    float64 `json:"budget_usd"`
	RemainingUSD float64 `json:"remaining_usd"`
	NextMonth    float64 `json:"next_month"`
}

func sourceMonthN(ctx context.Context, q db.Querier, source, month string, now float64) (int64, error) {
	var committed, carry int64
	if err := q.QueryRowContext(ctx,
		"SELECT COALESCE(SUM("+committedSQL+"), 0) FROM usage WHERE source = ? AND month = ?",
		source, month).Scan(&committed); err != nil {
		return 0, err
	}
	if err := q.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(reservation_nusd), 0) FROM usage WHERE source = ? AND month < ? AND state IN ('reserved','unknown') "+
			"AND expires_at > ? AND budget_class <> ?", source, month, now, ClassSubscription).Scan(&carry); err != nil {
		return 0, err
	}
	return committed + carry, nil
}

func SourceMonthCommitted(ctx context.Context, q db.Querier, source string, now float64, loc *time.Location) (float64, error) {
	n, err := sourceMonthN(ctx, q, source, MonthKey(now, loc), now)
	return usdOf(n), err
}

func (s *Service) RecordClassificationDeferral(ctx context.Context, monthStart float64, caller, itemKey string) error {
	if strings.TrimSpace(caller) == "" || strings.TrimSpace(itemKey) == "" {
		return errors.New("inference: a classification deferral needs a caller and an item key")
	}
	now := s.now()
	return s.db.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO inference_classification_deferrals (month_start, first_at, last_at, last_caller, last_item_key) VALUES (?, ?, ?, ?, ?) "+
				"ON CONFLICT (month_start) DO UPDATE SET last_at = excluded.last_at, last_caller = excluded.last_caller, last_item_key = excluded.last_item_key",
			monthStart, now, now, caller, itemKey)
		return err
	})
}

func ClassificationDeferrals(ctx context.Context, q db.Querier, now float64, loc *time.Location) (*ClassificationDeferral, error) {
	out := ClassificationDeferral{MonthStart: MonthStart(now, loc)}
	err := q.QueryRowContext(ctx,
		"SELECT first_at, last_at, last_caller, last_item_key FROM inference_classification_deferrals WHERE month_start = ?",
		out.MonthStart).Scan(&out.FirstAt, &out.LastAt, &out.LastCaller, &out.LastItemKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	status, err := StatusAt(ctx, q, now, loc)
	if err != nil {
		return nil, err
	}
	room, err := reservationNUSD(pricing.HaikuID, classificationRoomPromptTokens, classificationRoomOutputTokens, 0, prompts.TTL5m)
	if err != nil {
		return nil, err
	}
	if nanoUSD(status.RemainingUSD) >= room {
		return nil, nil
	}
	out.BudgetUSD, out.RemainingUSD, out.NextMonth = status.BudgetUSD, status.RemainingUSD, status.NextMonth
	return &out, nil
}
