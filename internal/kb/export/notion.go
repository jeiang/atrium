package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/kb/render"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/notion"
)

const (
	TargetNotion = "notion"
	targetPrefix = "export:notion:"
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

func intent(target string, after map[string]any) ledger.New {
	return ledger.New{Origin: "export", Operation: "kb_export", OperationKey: "kb_export:" + ledger.NewKey(), Target: target, Action: jsonOf(after),
		Resources: []ledger.Resource{{Key: target}}}
}

func settleFailure(ctx context.Context, store *ledger.Store, ticket ledger.Ticket, target string, hash string, err error) error {
	var refused *notion.Error
	if errors.As(err, &refused) && refused.Status >= 400 && refused.Status < 500 {
		if serr := store.Settle(ctx, ticket, ledger.Result{State: ledger.Failed, Target: target, Error: err.Error()}, nil); serr != nil {
			return serr
		}
		return err
	}
	unknown := ledger.Result{State: ledger.Unknown, Target: target, After: jsonOf(map[string]any{"hash": hash}), Error: err.Error()}
	if serr := store.Settle(ctx, ticket, unknown, nil); serr != nil {
		return serr
	}
	return err
}

func syncNotion(ctx context.Context, store *ledger.Store, now float64, client *notion.Client, parent string, entries []pageEntry) (Result, error) {
	var out Result
	d := store.DB
	stored, err := loadPages(ctx, d.Read, TargetNotion)
	if err != nil {
		return out, err
	}
	ledgerCtx := context.WithoutCancel(ctx)
	current := make(map[int64]bool, len(entries))
	for _, entry := range entries {
		current[entry.recordID] = true
		target := targetPrefix + cite(entry.recordID)
		prior, found := stored[entry.recordID]
		if found && prior.hash == entry.page.Hash {
			out.Skipped++
			continue
		}
		if found {
			ticket, err := store.Begin(ledgerCtx, intent(target, map[string]any{"hash": entry.page.Hash}), nil)
			if err != nil {
				return out, err
			}
			if err := client.SetTitle(ctx, prior.remoteID, entry.page.Title); err != nil {
				return out, settleFailure(ledgerCtx, store, ticket, target, entry.page.Hash, err)
			}
			if err := client.UpdateMarkdown(ctx, prior.remoteID, notion.ReplaceContent(entry.page.Markdown)); err != nil {
				return out, settleFailure(ledgerCtx, store, ticket, target, entry.page.Hash, err)
			}
			done := ledger.Result{State: ledger.Done, Target: target, After: jsonOf(map[string]any{"hash": entry.page.Hash, "page_id": prior.remoteID})}
			if err := store.Settle(ledgerCtx, ticket, done, nil); err != nil {
				return out, err
			}
			if err := savePage(ledgerCtx, d.Write, TargetNotion, entry.recordID, prior.remoteID, entry.page.Hash, now); err != nil {
				return out, err
			}
			out.Updated++
			continue
		}
		ticket, err := store.Begin(ledgerCtx, intent(target, map[string]any{"hash": entry.page.Hash}), nil)
		if err != nil {
			return out, err
		}
		page, err := client.CreatePage(ctx, map[string]any{
			"parent":     map[string]any{"page_id": parent},
			"properties": map[string]any{"title": map[string]any{"title": richText(entry.page.Title)}},
			"markdown":   entry.page.Markdown,
		})
		if err != nil {
			return out, settleFailure(ledgerCtx, store, ticket, target, entry.page.Hash, err)
		}
		done := ledger.Result{State: ledger.Done, Target: target, After: jsonOf(map[string]any{"hash": entry.page.Hash, "page_id": page.ID})}
		if err := store.Settle(ledgerCtx, ticket, done, nil); err != nil {
			return out, err
		}
		if err := savePage(ledgerCtx, d.Write, TargetNotion, entry.recordID, page.ID, entry.page.Hash, now); err != nil {
			return out, err
		}
		out.Created++
	}
	for recordID, prior := range stored {
		if current[recordID] {
			continue
		}
		target := targetPrefix + cite(recordID)
		ticket, err := store.Begin(ledgerCtx, intent(target, map[string]any{"trash": true}), nil)
		if err != nil {
			return out, err
		}
		if err := client.SetTrash(ctx, prior.remoteID, true); err != nil {
			return out, settleFailure(ledgerCtx, store, ticket, target, prior.hash, err)
		}
		done := ledger.Result{State: ledger.Done, Target: target, After: jsonOf(map[string]any{"trash": true, "page_id": prior.remoteID})}
		if err := store.Settle(ledgerCtx, ticket, done, nil); err != nil {
			return out, err
		}
		if err := deletePage(ledgerCtx, d.Write, TargetNotion, recordID); err != nil {
			return out, err
		}
		out.Removed++
	}
	return out, nil
}

func richText(text string) []any {
	return []any{map[string]any{"type": "text", "text": map[string]any{"content": text}}}
}

func jsonOf(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func blocked(ctx context.Context, q db.Querier) error {
	id, err := ledger.BlockedTarget(ctx, q, targetPrefix)
	if err != nil {
		return err
	}
	if id != 0 {
		return fmt.Errorf("export blocked by write #%d", id)
	}
	return nil
}
