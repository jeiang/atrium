package export

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/kb/render"
	"github.com/jeiang/atrium/internal/ledger"
)

type pageEntry struct {
	recordID int64
	page     render.Page
	path     string
	front    string
}

type storedPage struct {
	remoteID string
	hash     string
}

func cite(recordID int64) string {
	if recordID == 0 {
		return "index"
	}
	return "K" + strconv.FormatInt(recordID, 10)
}

func loadPages(ctx context.Context, q db.Querier, target string) (map[int64]storedPage, error) {
	rows, err := q.QueryContext(ctx, "SELECT record_id, remote_id, hash FROM export_pages WHERE target = ?", target)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int64]storedPage)
	for rows.Next() {
		var id int64
		var page storedPage
		if err := rows.Scan(&id, &page.remoteID, &page.hash); err != nil {
			return nil, err
		}
		out[id] = page
	}
	return out, rows.Err()
}

func savePage(ctx context.Context, q db.Querier, target string, recordID int64, remoteID, hash string, now float64) error {
	_, err := q.ExecContext(ctx,
		"INSERT OR REPLACE INTO export_pages (target, record_id, remote_id, hash, updated_at) VALUES (?, ?, ?, ?, ?)",
		target, recordID, remoteID, hash, now)
	return err
}

func deletePage(ctx context.Context, q db.Querier, target string, recordID int64) error {
	_, err := q.ExecContext(ctx, "DELETE FROM export_pages WHERE target = ? AND record_id = ?", target, recordID)
	return err
}

func jsonOf(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func blockedPrefix(ctx context.Context, q db.Querier, prefix string) error {
	id, err := ledger.BlockedTarget(ctx, q, prefix)
	if err != nil {
		return err
	}
	if id != 0 {
		return fmt.Errorf("export blocked by write #%d", id)
	}
	return nil
}

func Resolve(s *ledger.Store) []ledger.ResolveDefinition {
	return obsidianResolves(s)
}

func Undo(s *ledger.Store) []ledger.UndoDefinition {
	return obsidianUndo(s)
}
