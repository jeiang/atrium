package registry

import (
	"context"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/db"
)

func Taints(f tool.Flags) bool { return f.Has(tool.PrivateData) }

func Dropped(f tool.Flags, tainted bool) bool { return tainted && f.Has(tool.ExternalEffect) }

const taintedSQL = `WITH RECURSIVE chain(id) AS (
	SELECT ? UNION SELECT t.parent_thread_id FROM threads t JOIN chain c ON t.id = c.id WHERE t.parent_thread_id IS NOT NULL
) SELECT EXISTS (
	SELECT 1 FROM thread_messages m, json_each(m.content_json) b
	WHERE m.thread_id IN (SELECT id FROM chain) AND m.role = ? AND m.superseded = 0
	AND json_extract(b.value, '$.type') = 'tool_use' AND json_extract(b.value, '$.name') IN (`

func (r *Registry) Tainted(ctx context.Context, q db.Querier, threadID int64) (bool, error) {
	all, err := r.All()
	if err != nil {
		return false, err
	}
	args := []any{threadID, journal.Assistant}
	for _, t := range all {
		if Taints(t.Flags) {
			args = append(args, t.Name)
		}
	}
	if len(args) == 2 {
		return false, nil
	}
	query := taintedSQL + strings.TrimSuffix(strings.Repeat("?,", len(args)-2), ",") + "))"
	var found bool
	err = q.QueryRowContext(ctx, query, args...).Scan(&found)
	return found, err
}
