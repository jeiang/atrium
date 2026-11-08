package notiontools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notion"
)

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

type pageUndo struct {
	Op                string `json:"op"`
	PageID            string `json:"page_id"`
	AppliedEditedTime string `json:"applied_edited_time"`
}

type restoreUndo struct {
	pageUndo
	PreviousMarkdown string `json:"previous_markdown"`
}

type moveUndo struct {
	pageUndo
	PreviousParentID string `json:"previous_parent_id"`
}

type aliasesUndo struct {
	Op              string   `json:"op"`
	PageID          string   `json:"page_id"`
	PreviousAliases []string `json:"previous_aliases"`
}

func refuse(err error) error {
	if converted := tool.Domain[*notion.Error, writes.UndoError](err); converted != err {
		return converted
	}
	if msg, ok := tool.Message(err); ok {
		return writes.UndoError(msg)
	}
	return err
}

func fresh(ctx context.Context, client *notion.Client, pageID, applied string) error {
	live, err := client.GetPage(ctx, pageID)
	if err != nil {
		return err
	}
	if live.LastEditedTime != applied {
		return writes.UndoError("the page changed in Notion after this write; refusing to overwrite it")
	}
	return nil
}

func guarded(env config.Env, fn func(ctx context.Context, q db.Querier, client *notion.Client, payload json.RawMessage) (any, error)) writes.Undoer {
	return func(ctx context.Context, q db.Querier, payload json.RawMessage) (any, error) {
		client, err := makeClient(ctx, q, env)
		if err != nil {
			return nil, refuse(err)
		}
		out, err := fn(ctx, q, client, payload)
		if err != nil {
			return nil, refuse(err)
		}
		return out, nil
	}
}

func trashing(value bool, key string) func(context.Context, db.Querier, *notion.Client, json.RawMessage) (any, error) {
	return func(ctx context.Context, q db.Querier, client *notion.Client, payload json.RawMessage) (any, error) {
		var u pageUndo
		if err := json.Unmarshal(payload, &u); err != nil {
			return nil, writes.UndoError("notion undo payload is invalid")
		}
		if err := fresh(ctx, client, u.PageID, u.AppliedEditedTime); err != nil {
			return nil, err
		}
		if err := client.SetTrash(ctx, u.PageID, value); err != nil {
			return nil, err
		}
		if err := notion.UpsertWiki(ctx, q, u.PageID, now(), notion.WikiFields{InTrash: &value}); err != nil {
			return nil, err
		}
		return map[string]any{key: u.PageID}, nil
	}
}

func undoRestorePage(ctx context.Context, _ db.Querier, client *notion.Client, payload json.RawMessage) (any, error) {
	var u restoreUndo
	if err := json.Unmarshal(payload, &u); err != nil {
		return nil, writes.UndoError("notion undo payload is invalid")
	}
	if err := fresh(ctx, client, u.PageID, u.AppliedEditedTime); err != nil {
		return nil, err
	}
	if err := client.UpdateMarkdown(ctx, u.PageID, replaceContent(u.PreviousMarkdown)); err != nil {
		return nil, err
	}
	return map[string]any{"restored": u.PageID}, nil
}

func undoMovePage(ctx context.Context, q db.Querier, client *notion.Client, payload json.RawMessage) (any, error) {
	var u moveUndo
	if err := json.Unmarshal(payload, &u); err != nil {
		return nil, writes.UndoError("notion undo payload is invalid")
	}
	if err := fresh(ctx, client, u.PageID, u.AppliedEditedTime); err != nil {
		return nil, err
	}
	if err := client.MovePage(ctx, u.PageID, map[string]any{"type": "page_id", "page_id": u.PreviousParentID}); err != nil {
		return nil, err
	}
	if err := notion.UpsertWiki(ctx, q, u.PageID, now(), notion.WikiFields{HubID: &u.PreviousParentID}); err != nil {
		return nil, err
	}
	return map[string]any{"moved": u.PageID, "parent_page_id": u.PreviousParentID}, nil
}

func undoAliasList(ctx context.Context, q db.Querier, payload json.RawMessage) (any, error) {
	var u aliasesUndo
	if err := json.Unmarshal(payload, &u); err != nil {
		return nil, writes.UndoError("notion undo payload is invalid")
	}
	if err := notion.UpsertWiki(ctx, q, u.PageID, now(), notion.WikiFields{Aliases: u.PreviousAliases, SetAliases: true}); err != nil {
		return nil, err
	}
	return map[string]any{"aliases": strs(u.PreviousAliases)}, nil
}

func RegisterUndoers(r *writes.Registry, env config.Env) {
	r.Register(undoRestore, guarded(env, undoRestorePage))
	r.Register(undoTrash, guarded(env, trashing(true, "trashed")))
	r.Register(undoUntrash, guarded(env, trashing(false, "restored")))
	r.Register(undoMove, guarded(env, undoMovePage))
	r.Register(undoAliases, undoAliasList)
}

func Capture(ctx context.Context, tc *tool.Context, name, note string) (map[string]any, error) {
	wiki, err := wikiOf(ctx, tc)
	if err != nil {
		return nil, err
	}
	date, err := today(ctx, tc)
	if err != nil {
		return nil, err
	}
	client, err := makeClient(ctx, tc.DB.Read, tc.Env)
	if err != nil {
		return nil, err
	}
	logID, err := wiki.LogPageID(ctx, tc.DB, client, wiki.Root)
	if err != nil {
		return nil, notionError(err)
	}
	before, err := client.PageMarkdown(ctx, logID)
	if err != nil {
		return nil, notionError(err)
	}
	if before.Truncated {
		return nil, tool.Error("the Wiki Log page is too large to update safely; archive it and let atrium start a new one")
	}
	id, err := beginWrite(ctx, tc, name, logID)
	if err != nil {
		return nil, err
	}
	writer := &notion.Writer{DB: tc.DB, Client: client}
	if err := writer.Capture(ctx, []string{notion.LogLine(date, "note", note, "")}); err != nil {
		var failure notion.WikiError
		if errors.As(err, &failure) {
			_ = writes.Discard(ctx, tc.DB.Write, id)
		}
		return nil, notionError(err)
	}
	edited, err := editedTime(ctx, client, logID)
	if err != nil {
		return nil, notionError(err)
	}
	w := write{
		id: id, tool: name, target: logID,
		before: map[string]any{"markdown": before.Markdown}, after: map[string]any{"note": note},
		undo: restoreUndo{pageUndo: pageUndo{Op: undoRestore, PageID: logID, AppliedEditedTime: edited}, PreviousMarkdown: before.Markdown},
	}
	if _, err := w.commit(ctx, tc, func(*sql.Tx) error { return nil }); err != nil {
		return nil, err
	}
	return map[string]any{"note": note, "page_id": logID, "write_id": id}, nil
}
