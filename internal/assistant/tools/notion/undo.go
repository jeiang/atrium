package notiontools

import (
	"context"
	"errors"
	"time"

	"github.com/jeiang/atrium/internal/assistant/tool"
	proposetools "github.com/jeiang/atrium/internal/assistant/tools/propose"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/py"
)

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func infoDict(info map[string]any) py.Dict {
	d := make(py.Dict, 0, len(info))
	for k, v := range info {
		d = append(d, py.Pair{Key: k, Value: v})
	}
	return d
}

func refuse(err error) error {
	var failure *notion.Error
	if errors.As(err, &failure) {
		return writes.UndoError("notion: " + err.Error())
	}
	if msg, ok := tool.Message(err); ok {
		return writes.UndoError(msg)
	}
	return err
}

func fresh(ctx context.Context, client *notion.Client, info map[string]any) error {
	live, err := client.GetPage(ctx, info["page_id"].(string))
	if err != nil {
		return err
	}
	if dictGet(live, "last_edited_time") != info["applied_edited_time"] {
		return writes.UndoError("the page changed in Notion after this write; refusing to overwrite it")
	}
	return nil
}

func guarded(env config.Env, fn func(ctx context.Context, q db.Querier, client *notion.Client, info map[string]any) (any, error)) writes.Undoer {
	return func(ctx context.Context, q db.Querier, info map[string]any) (any, error) {
		client, err := makeClient(ctx, q, env)
		if err != nil {
			return nil, refuse(err)
		}
		out, err := fn(ctx, q, client, info)
		if err != nil {
			return nil, refuse(err)
		}
		return out, nil
	}
}

func trashing(value bool, key string) func(context.Context, db.Querier, *notion.Client, map[string]any) (any, error) {
	return func(ctx context.Context, q db.Querier, client *notion.Client, info map[string]any) (any, error) {
		if err := fresh(ctx, client, info); err != nil {
			return nil, err
		}
		id := info["page_id"].(string)
		if _, err := client.SetTrash(ctx, id, value); err != nil {
			return nil, err
		}
		if err := notion.UpsertWiki(ctx, q, id, now(), notion.WikiFields{InTrash: &value}); err != nil {
			return nil, err
		}
		return py.Dict{{Key: key, Value: id}}, nil
	}
}

func undoRestorePage(ctx context.Context, _ db.Querier, client *notion.Client, info map[string]any) (any, error) {
	if err := fresh(ctx, client, info); err != nil {
		return nil, err
	}
	id := info["page_id"].(string)
	if _, err := client.UpdateMarkdown(ctx, id, replaceContent(info["previous_markdown"].(string))); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "restored", Value: id}}, nil
}

func undoMovePage(ctx context.Context, q db.Querier, client *notion.Client, info map[string]any) (any, error) {
	if err := fresh(ctx, client, info); err != nil {
		return nil, err
	}
	id, previous := info["page_id"].(string), info["previous_parent_id"].(string)
	if _, err := client.MovePage(ctx, id, py.Dict{{Key: "type", Value: "page_id"}, {Key: "page_id", Value: previous}}); err != nil {
		return nil, err
	}
	if err := notion.UpsertWiki(ctx, q, id, now(), notion.WikiFields{HubID: &previous}); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "moved", Value: id}, {Key: "parent_page_id", Value: previous}}, nil
}

func undoAliasList(ctx context.Context, q db.Querier, info map[string]any) (any, error) {
	id := info["page_id"].(string)
	raw, _ := info["previous_aliases"].([]any)
	previous := make([]string, len(raw))
	for i, a := range raw {
		previous[i], _ = a.(string)
	}
	if err := notion.UpsertWiki(ctx, q, id, now(), notion.WikiFields{Aliases: previous, SetAliases: true}); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "aliases", Value: strs(previous)}}, nil
}

func RegisterUndoers(r *writes.Registry, env config.Env) {
	r.Register(undoRestore, guarded(env, undoRestorePage))
	r.Register(undoTrash, guarded(env, trashing(true, "trashed")))
	r.Register(undoUntrash, guarded(env, trashing(false, "restored")))
	r.Register(undoMove, guarded(env, undoMovePage))
	r.Register(undoAliases, undoAliasList)
}

func pageArg(payload py.Dict, key string) (string, error) {
	v, _ := dictGet(payload, key).(string)
	if py.Strip(v) == "" {
		return "", tool.Error("payload." + key + " must be a Notion page id")
	}
	return py.Strip(v), nil
}

func proposed(err error) error {
	var pe notion.ProposalError
	if errors.As(err, &pe) {
		return tool.Error(string(pe))
	}
	return err
}

func proposer(tc *tool.Context) *notion.Proposer {
	return &notion.Proposer{DB: tc.DB, Now: tc.Now}
}

func proposeEdit(ctx context.Context, tc *tool.Context, payload py.Dict) (*int64, error) {
	instruction, _ := dictGet(payload, "instruction").(string)
	if py.Strip(instruction) == "" {
		return nil, tool.Error("payload.instruction must be text")
	}
	page, err := pageArg(payload, "page_id")
	if err != nil {
		return nil, err
	}
	id, err := proposer(tc).ProposeEdit(ctx, page, instruction)
	return id, proposed(err)
}

func proposeLink(ctx context.Context, tc *tool.Context, payload py.Dict) (*int64, error) {
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
	proposetools.Register(notion.KindEdit, proposeEdit)
	proposetools.Register(notion.KindLink, proposeLink)
}
