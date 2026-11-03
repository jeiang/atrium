package sources

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/calendar/candidates"
	"github.com/jeiang/atrium/internal/db"
)

func Calendar() []attention.Source { return []attention.Source{attention.SourceFunc(eventCandidates)} }

func eventCandidates(ctx context.Context, q db.Querier, _ time.Time, _ *time.Location) ([]attention.Item, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT e.message_id, e.reason, e.ts, COALESCE(m.subject, '') FROM event_candidates e LEFT JOIN messages m ON m.id = e.message_id "+
			"WHERE e.status = ? ORDER BY e.ts, e.message_id LIMIT ?", candidates.StatusQueued, attention.QueueLimit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (attention.Item, error) {
		var id int64
		var reason, subject string
		it := attention.Item{Kind: attention.KindEvent, Class: attention.NeedsDecision}
		err := r.Scan(&id, &reason, &it.TS, &subject)
		it.SourceID = strconv.FormatInt(id, 10)
		it.Title = subject
		if it.Title == "" {
			it.Title = reason
		}
		return it, err
	})
}
