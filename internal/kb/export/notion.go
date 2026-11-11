package export

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

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

const (
	opCreate  = "kb_export_create"
	opTitle   = "kb_export_title"
	opContent = "kb_export_content"
	opTrash   = "kb_export_trash"
)

func step(target, operation, key string, payload map[string]any) ledger.New {
	return ledger.New{Origin: "export", Operation: operation, OperationKey: key, Target: target,
		Action: jsonOf(payload), Resources: []ledger.Resource{{Key: target}}}
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

func runStep(ctx context.Context, store *ledger.Store, n ledger.New, hash string, call func() (map[string]any, error), done ledger.Mutation) error {
	ticket, err := store.Begin(ctx, n, nil)
	if err != nil {
		return err
	}
	after, err := call()
	if err != nil {
		return settleFailure(ctx, store, ticket, n.Target, hash, err)
	}
	return store.Settle(ctx, ticket, ledger.Result{State: ledger.Done, Target: n.Target, After: jsonOf(after)}, done)
}

func titleApplied(ctx context.Context, store *ledger.Store, target, hash string) (bool, error) {
	found, err := store.List(ctx, ledger.Filter{Operation: opTitle, Target: target, Limit: 1})
	if err != nil || len(found) == 0 {
		return false, err
	}
	e := found[0]
	if e.State != ledger.Done {
		return false, nil
	}
	var after struct {
		Hash string `json:"hash"`
	}
	if json.Unmarshal(e.After, &after) == nil && after.Hash == hash {
		return true, nil
	}
	return false, nil
}

func syncNotion(ctx context.Context, store *ledger.Store, now float64, client *notion.Client, parent string, entries []pageEntry) (Result, error) {
	var out Result
	d := store.DB
	stored, err := loadPages(ctx, d.Read, TargetNotion)
	if err != nil {
		return out, err
	}
	current := make(map[int64]bool, len(entries))
	for _, entry := range entries {
		current[entry.recordID] = true
		target := targetPrefix + cite(entry.recordID)
		prior, found := stored[entry.recordID]
		if found && prior.hash == entry.page.Hash {
			out.Skipped++
			continue
		}
		key := "export:notion:" + cite(entry.recordID) + ":" + entry.page.Hash
		if found {
			applied, err := titleApplied(ctx, store, target, entry.page.Hash)
			if err != nil {
				return out, err
			}
			if !applied {
				if err := runStep(ctx, store, step(target, opTitle, key+":title", map[string]any{"hash": entry.page.Hash, "page_id": prior.remoteID}), entry.page.Hash, func() (map[string]any, error) {
					return map[string]any{"hash": entry.page.Hash, "page_id": prior.remoteID}, client.SetTitle(ctx, prior.remoteID, entry.page.Title)
				}, nil); err != nil {
					return out, err
				}
			}
			if err := runStep(ctx, store, step(target, opContent, key+":content", map[string]any{"hash": entry.page.Hash, "page_id": prior.remoteID}), entry.page.Hash, func() (map[string]any, error) {
				return map[string]any{"hash": entry.page.Hash, "page_id": prior.remoteID}, client.UpdateMarkdown(ctx, prior.remoteID, notion.ReplaceContent(entry.page.Markdown))
			}, func(ctx context.Context, tx *sql.Tx) error {
				return savePage(ctx, tx, TargetNotion, entry.recordID, prior.remoteID, entry.page.Hash, now)
			}); err != nil {
				return out, err
			}
			out.Updated++
			continue
		}
		var pageID string
		if err := runStep(ctx, store, step(target, opCreate, key+":create", map[string]any{"hash": entry.page.Hash}), entry.page.Hash, func() (map[string]any, error) {
			page, err := client.CreatePage(ctx, map[string]any{
				"parent":     map[string]any{"page_id": parent},
				"properties": map[string]any{"title": map[string]any{"title": richText(entry.page.Title)}},
				"markdown":   entry.page.Markdown,
			})
			if err != nil {
				return nil, err
			}
			pageID = page.ID
			return map[string]any{"hash": entry.page.Hash, "page_id": page.ID}, nil
		}, func(ctx context.Context, tx *sql.Tx) error {
			return savePage(ctx, tx, TargetNotion, entry.recordID, pageID, entry.page.Hash, now)
		}); err != nil {
			return out, err
		}
		out.Created++
	}
	for recordID, prior := range stored {
		if current[recordID] {
			continue
		}
		target := targetPrefix + cite(recordID)
		key := "export:notion:" + cite(recordID) + ":" + prior.hash + ":trash"
		if err := runStep(ctx, store, step(target, opTrash, key, map[string]any{"trash": true, "page_id": prior.remoteID}), prior.hash, func() (map[string]any, error) {
			return map[string]any{"trash": true, "page_id": prior.remoteID}, client.SetTrash(ctx, prior.remoteID, true)
		}, func(ctx context.Context, tx *sql.Tx) error {
			return deletePage(ctx, tx, TargetNotion, recordID)
		}); err != nil {
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
	return blockedPrefix(ctx, q, targetPrefix)
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

const UndoNotionTrash = "notion_trash"

type trashUndo struct {
	Op     string `json:"op"`
	PageID string `json:"page_id"`
}

func recordOf(target string) (int64, bool) {
	rest, ok := strings.CutPrefix(target, targetPrefix)
	if !ok {
		return 0, false
	}
	if rest == "index" {
		return 0, true
	}
	if !strings.HasPrefix(rest, "K") {
		return 0, false
	}
	id, err := strconv.ParseInt(rest[1:], 10, 64)
	return id, err == nil
}

func RegisterLedgerResolves(s *ledger.Store) error {
	if err := s.RegisterResolve(opCreate, func(ctx context.Context, tx *sql.Tx, e ledger.Effect, r ledger.Resolution) error {
		if r.Outcome != "applied" && r.Outcome != "partial" {
			return nil
		}
		var ev struct {
			PageID string `json:"page_id"`
			Hash   string `json:"hash"`
		}
		if len(r.Evidence) == 0 || json.Unmarshal(r.Evidence, &ev) != nil || ev.PageID == "" || ev.Hash == "" {
			return ledger.Refused("resolving an export create needs evidence with page_id and hash")
		}
		var action struct {
			Hash string `json:"hash"`
		}
		if err := json.Unmarshal(e.Action, &action); err != nil {
			return err
		}
		if action.Hash != ev.Hash {
			return ledger.Refused("the evidence hash does not match the attempted export")
		}
		recordID, ok := recordOf(e.Target)
		if !ok {
			return ledger.Refused("the effect target is not an export record")
		}
		if err := savePage(ctx, tx, TargetNotion, recordID, ev.PageID, ev.Hash, s.Now()); err != nil {
			return err
		}
		return s.SetUndo(ctx, tx, e.ID, jsonOf(trashUndo{Op: UndoNotionTrash, PageID: ev.PageID}))
	}); err != nil {
		return err
	}
	return s.RegisterUndo(UndoNotionTrash, ledger.Undoer{
		Validate: func(raw json.RawMessage) error {
			var u trashUndo
			if err := json.Unmarshal(raw, &u); err != nil || u.PageID == "" {
				return ledger.Refused("notion_trash without a page id")
			}
			return nil
		},
		External: func(ctx context.Context, raw json.RawMessage) (ledger.Result, error) {
			var u trashUndo
			if err := json.Unmarshal(raw, &u); err != nil || u.PageID == "" {
				return ledger.Result{}, ledger.Refused("notion_trash without a page id")
			}
			client, err := notion.NewClient(ctx, s.DB.Read)
			if err != nil {
				return ledger.Result{}, err
			}
			defer client.Close()
			if err := client.SetTrash(ctx, u.PageID, true); err != nil {
				var refused *notion.Error
				if errors.As(err, &refused) && refused.Status >= 400 && refused.Status < 500 {
					return ledger.Result{State: ledger.Failed, Error: err.Error()}, nil
				}
				return ledger.Result{State: ledger.Unknown, Error: err.Error()}, nil
			}
			return ledger.Result{State: ledger.Done, After: jsonOf(map[string]any{"trashed": u.PageID})}, nil
		},
		After: func(ctx context.Context, tx *sql.Tx, e ledger.Effect, result ledger.Result) error {
			if result.State != ledger.Done {
				return nil
			}
			recordID, ok := recordOf(e.Target)
			if !ok {
				return nil
			}
			return deletePage(ctx, tx, TargetNotion, recordID)
		},
	})
}
