package sources

import (
	"context"
	"strconv"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
)

type packages struct{}

func Packages() attention.Source { return packages{} }

func (packages) Name() string { return "packages" }

func (packages) Items(ctx context.Context, q db.Querier, _ float64) ([]attention.Item, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, merchant, status_text, tracking_number, first_seen FROM packages "+
			"WHERE dismissed = 0 AND status NOT IN ('delivered', 'returned') ORDER BY first_seen")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []attention.Item
	for rows.Next() {
		var id int64
		var merchant, status, tracking string
		var seen float64
		if err := rows.Scan(&id, &merchant, &status, &tracking, &seen); err != nil {
			return nil, err
		}
		title := merchant
		if title == "" {
			title = "Package"
		}
		items = append(items, attention.Item{
			Kind: "package", SourceID: strconv.FormatInt(id, 10), Title: title, Detail: status + " " + tracking,
			Created: seen, Group: attention.Upcoming,
		})
	}
	return items, rows.Err()
}
