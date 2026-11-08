package notiontools

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant/tool"
	proposetools "github.com/jeiang/atrium/internal/assistant/tools/propose"
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
		client, err := makeClient(ctx, q)
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

func pageArg(payload map[string]any, key string) (string, error) {
	v, _ := payload[key].(string)
	if strings.TrimSpace(v) == "" {
		return "", tool.Error("payload." + key + " must be a Notion page id")
	}
	return strings.TrimSpace(v), nil
}

func proposed(err error) error {
	return tool.Domain[notion.ProposalError, tool.Error](err)
}

func proposer(tc *tool.Context) *notion.Proposer {
	return &notion.Proposer{DB: tc.DB, Now: tc.Now}
}

func proposeEdit(ctx context.Context, tc *tool.Context, payload map[string]any) (*int64, error) {
	instruction, _ := payload["instruction"].(string)
	if strings.TrimSpace(instruction) == "" {
		return nil, tool.Error("payload.instruction must be text")
	}
	page, err := pageArg(payload, "page_id")
	if err != nil {
		return nil, err
	}
	id, err := proposer(tc).ProposeEdit(ctx, page, instruction)
	return id, proposed(err)
}

func proposeLink(ctx context.Context, tc *tool.Context, payload map[string]any) (*int64, error) {
	source, err := pageArg(payload, "source_id")
	if err != nil {
		return nil, err
	}
	target, err := pageArg(payload, "target_id")
	if err != nil {
		return nil, err
	}
	id, err := proposer(tc).ProposeLink(ctx, source, target)
	return id, proposed(err)
}

func RegisterProposalKinds() {
	proposetools.Register(notion.KindEdit, "{page_id, instruction}: propose an edit to a Notion page for the operator to approve", proposeEdit)
	proposetools.Register(notion.KindLink, "{source_id, target_id}: propose linking two related Notion pages", proposeLink)
}
