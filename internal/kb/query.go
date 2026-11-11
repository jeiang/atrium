package kb

import (
	"context"
	"database/sql"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/writes"
)

func VisibleClause(alias string) string {
	return alias + ".private = 0 AND (" + alias + ".project_id IS NULL OR " + alias + ".project_id NOT IN (" + projects.PrivateIDsSQL() + "))"
}

func runArgs(runIDs []int64) (string, []any) {
	if len(runIDs) == 0 {
		return "", nil
	}
	args := make([]any, len(runIDs))
	for i, id := range runIDs {
		args[i] = id
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(runIDs)), ","), args
}

func CountRunRecords(ctx context.Context, q db.Querier, runIDs []int64) (int, error) {
	marks, args := runArgs(runIDs)
	if marks == "" {
		return 0, nil
	}
	var n int
	err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM kb_records r WHERE r.source_run_id IN ("+marks+") AND "+VisibleClause("r"), args...).Scan(&n)
	return n, err
}

func CountRunLessons(ctx context.Context, q db.Querier, runIDs []int64) (int, error) {
	marks, args := runArgs(runIDs)
	if marks == "" {
		return 0, nil
	}
	var n int
	err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM kb_records r WHERE r.source_run_id IN ("+marks+") AND r.retracted_at IS NULL AND r.title LIKE 'Lesson: %' AND "+VisibleClause("r"), args...).Scan(&n)
	return n, err
}

func CountRunMarked(ctx context.Context, q db.Querier, runIDs []int64) (int, error) {
	marks, args := runArgs(runIDs)
	if marks == "" {
		return 0, nil
	}
	var n int
	err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM kb_records r WHERE r.source_run_id IN ("+marks+") AND r.retracted_at IS NULL AND r.marks != 0 AND "+VisibleClause("r"), args...).Scan(&n)
	return n, err
}

func CountRunWrites(ctx context.Context, q db.Querier, runIDs []int64, toolName string, excludeSourceRuns bool) (int, error) {
	marks, args := runArgs(runIDs)
	if marks == "" {
		return 0, nil
	}
	query := "SELECT COUNT(*) FROM " + writes.FromClause("w") + " JOIN kb_records r ON w.target = 'kb:' || r.id WHERE w.run_id IN (" + marks + ") AND w.tool = ?"
	args = append(args, toolName)
	if excludeSourceRuns {
		query += " AND r.id NOT IN (SELECT id FROM kb_records WHERE source_run_id IN (" + marks + "))"
		for _, id := range runIDs {
			args = append(args, id)
		}
	}
	query += " AND " + VisibleClause("r")
	var n int
	err := q.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

type Conflict struct {
	ID     int64
	Title  string
	Thread *int64
	Marks  tool.Marks
}

func RunConflicts(ctx context.Context, q db.Querier, runIDs []int64, aliasesLike string) ([]Conflict, error) {
	marks, args := runArgs(runIDs)
	if marks == "" {
		return nil, nil
	}
	args = append(args, aliasesLike)
	rows, err := q.QueryContext(ctx,
		"SELECT r.id, r.title, r.source_thread_id, r.marks FROM kb_records r WHERE r.source_run_id IN ("+marks+") AND r.retracted_at IS NULL AND r.aliases LIKE ? AND "+VisibleClause("r")+" ORDER BY r.id LIMIT 2", args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Conflict, error) {
		var c Conflict
		var marks int64
		err := r.Scan(&c.ID, &c.Title, &c.Thread, &marks)
		c.Marks = tool.Marks(marks)
		return c, err
	})
}

type IndexEntry struct {
	ID        int64
	Title     string
	Scope     string
	Writer    string
	Confirmed bool
	Marks     tool.Marks
}

func RecordIndex(ctx context.Context, q db.Querier, now float64, allowed tool.Marks) ([]IndexEntry, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT r.id, r.title, r.scope, r.writer, r.confirmed_at IS NOT NULL, r.marks FROM kb_records r"+projects.JoinClause("p", "r")+
			" WHERE r.retracted_at IS NULL AND (r.expires_at IS NULL OR r.expires_at > ?) "+
			"AND NOT ((r.private = 1 OR COALESCE(p.private, 0) = 1) AND (r.project_id IS NULL OR r.project_id != 0)) "+
			"AND (r.marks & ?) = 0 ORDER BY r.updated_at DESC LIMIT 200", now, ^int64(allowed))
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (IndexEntry, error) {
		var e IndexEntry
		var marks int64
		err := r.Scan(&e.ID, &e.Title, &e.Scope, &e.Writer, &e.Confirmed, &marks)
		e.Marks = tool.Marks(marks)
		return e, err
	})
}

func Title(ctx context.Context, q db.Querier, id int64) (string, error) {
	var title string
	err := q.QueryRowContext(ctx, "SELECT title FROM kb_records WHERE id = ?", id).Scan(&title)
	return title, err
}
