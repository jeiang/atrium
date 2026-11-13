package sources

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/timex"
)

const ClassificationBudgetKind = "classification_budget"

type classificationBudget struct{}

func ClassificationBudget() attention.Source { return classificationBudget{} }

func (classificationBudget) Name() string { return ClassificationBudgetKind }

func (classificationBudget) Items(ctx context.Context, q db.Querier, now float64) ([]attention.Item, error) {
	loc, err := inference.Location(ctx, q)
	if err != nil {
		return nil, err
	}
	found, err := inference.ClassificationDeferrals(ctx, q, now, loc)
	if err != nil || found == nil {
		return nil, err
	}
	at := func(ts float64) string {
		sec, us := timex.SplitTimestamp(ts)
		return time.Unix(sec, us*1000).In(loc).Format("2006-01-02 15:04")
	}
	return []attention.Item{{
		Kind:     ClassificationBudgetKind,
		SourceID: strconv.FormatFloat(found.MonthStart, 'f', -1, 64),
		Title:    "Model decisions are waiting for the monthly budget",
		Detail: fmt.Sprintf("Mail and server-ops classifications are paused with $%.2f of the $%.2f monthly budget left. First refused %s, last refused %s (%s). Raise the budget at /settings/usage or wait until %s.",
			found.RemainingUSD, found.BudgetUSD, at(found.FirstAt), at(found.LastAt), found.LastCaller, at(found.NextMonth)),
		Created: found.FirstAt,
		Group:   attention.NeedsDecision,
	}}, nil
}
