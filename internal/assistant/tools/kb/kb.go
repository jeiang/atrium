package kbtools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/tool"
)

var (
	writeBound = tool.NewBound(assistant.KeyKBWriteChars, "")
	scopeEnum  = []string{kb.ScopeWork, kb.ScopePersonal}
)

var markNames = []struct {
	mark tool.Marks
	name string
}{
	{tool.Untrusted, "untrusted"},
	{tool.PII, "pii"},
	{tool.Messages, "messages"},
	{tool.Personal, "personal"},
}

func markList(marks tool.Marks) []string {
	out := []string{}
	for _, m := range markNames {
		if marks&m.mark != 0 {
			out = append(out, m.name)
		}
	}
	return out
}

func readerProject(ctx context.Context, q db.Querier, threadID int64) (int64, error) {
	var project *int64
	err := q.QueryRowContext(ctx, "SELECT project_id FROM threads WHERE id = ?", threadID).Scan(&project)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if project == nil {
		return 0, nil
	}
	return *project, nil
}

func sourceMap(r kb.Record) any {
	if r.Origin != nil {
		return map[string]any{"origin": *r.Origin}
	}
	if r.SourceThreadID == nil {
		return nil
	}
	out := map[string]any{"thread_id": *r.SourceThreadID}
	if r.SourceSeq != nil {
		out["seq"] = *r.SourceSeq
	}
	if r.SourceRunID != nil {
		out["run_id"] = *r.SourceRunID
	}
	return out
}

func resultMap(r kb.Record) map[string]any {
	out := map[string]any{
		"id": r.ID, "cite": kb.Cite(r.ID), "title": r.Title, "aliases": r.Aliases, "scope": r.Scope,
		"writer": r.Writer, "confirmed": r.Confirmed(), "pinned": r.Pinned, "marks": markList(r.Marks),
		"updated": time.Unix(int64(r.UpdatedAt), 0).UTC().Format("2006-01-02"), "retracted": r.Retracted(),
	}
	if source := sourceMap(r); source != nil {
		out["source"] = source
	}
	return out
}

func search(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	query, ok, err := tool.TextArg(args, "query", true)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, tool.Error("query is required")
	}
	if len([]rune(query)) > 200 {
		return nil, tool.Error("query is limited to 200 characters")
	}
	scope, _, err := tool.TextArg(args, "scope", false)
	if err != nil {
		return nil, err
	}
	if scope != "" && !slices.Contains(scopeEnum, scope) {
		return nil, tool.Error("scope must be work or personal")
	}
	limit, err := tool.BoundedInt(args, "limit", 10, 20, 1)
	if err != nil {
		return nil, err
	}
	project, err := readerProject(ctx, tc.DB.Read, tc.ThreadID)
	if err != nil {
		return nil, err
	}
	embedTexts := func(ctx context.Context, texts []string) ([][]float64, error) {
		if tc.Embed == nil {
			return nil, &httpx.ServiceUnavailable{Msg: "no embedder configured"}
		}
		return tc.Embed(ctx, texts)
	}
	found, err := kb.Search(ctx, tc.DB.Read, kb.SearchFilter{
		Query: query, Scope: scope, Limit: int(limit), ProjectID: project, Now: tc.Now(), Embed: embedTexts,
	})
	if err != nil {
		return nil, err
	}
	results := make([]any, 0, len(found.Hits))
	var marks tool.Marks
	for _, h := range found.Hits {
		item := resultMap(h.Record)
		item["snippet"] = tool.Clip(h.Snippet, 600)
		results = append(results, item)
		marks |= h.Marks
	}
	out := map[string]any{"results": results, "semantic": found.Semantic}
	if len(results) == 0 {
		return out, nil
	}
	marks |= tool.Personal
	return tool.Marked{Value: out, Marks: marks}, nil
}

func get(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	raw, ok := args.Get("ids")
	if !ok {
		return nil, tool.Error("ids are required")
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 || len(list) > 10 {
		return nil, tool.Error("ids must hold between 1 and 10 record ids")
	}
	ids := make([]int64, 0, len(list))
	for _, entry := range list {
		n, ok, err := tool.IntArg(tool.Args{"id": entry}, "id", true)
		if err != nil || !ok {
			return nil, tool.Error("ids must be integers")
		}
		ids = append(ids, n)
	}
	project, err := readerProject(ctx, tc.DB.Read, tc.ThreadID)
	if err != nil {
		return nil, err
	}
	found, err := kb.Read(ctx, tc.DB.Read, kb.ReadFilter{IDs: ids, ProjectID: project, Now: tc.Now()})
	if err != nil {
		return nil, err
	}
	results := make([]any, 0, len(found))
	var marks tool.Marks
	for _, r := range found {
		item := resultMap(r)
		item["body"] = r.Body
		results = append(results, item)
		marks |= r.Marks
	}
	out := map[string]any{"results": results}
	if len(results) == 0 {
		return out, nil
	}
	marks |= tool.Personal
	return tool.Marked{Value: out, Marks: marks}, nil
}

func aliasesArg(args tool.Args) ([]string, error) {
	raw, ok := args.Get("aliases")
	if !ok || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok || len(list) > 10 {
		return nil, tool.Error("aliases must be at most 10 strings")
	}
	out := make([]string, 0, len(list))
	for _, entry := range list {
		text, ok := entry.(string)
		if !ok || len([]rune(text)) > 80 {
			return nil, tool.Error("each alias must be a string of at most 80 characters")
		}
		out = append(out, text)
	}
	return out, nil
}

func sourceArg(args tool.Args) (*kb.Source, error) {
	raw, ok := args.Get("source")
	if !ok || raw == nil {
		return nil, nil
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return nil, tool.Error("source must be an object")
	}
	threadID, ok, err := tool.IntArg(tool.Args{"thread_id": fields["thread_id"]}, "thread_id", true)
	if err != nil || !ok {
		return nil, tool.Error("source.thread_id is required")
	}
	src := &kb.Source{ThreadID: threadID}
	if fields["seq"] != nil {
		seq, ok, err := tool.IntArg(tool.Args{"seq": fields["seq"]}, "seq", true)
		if err != nil || !ok {
			return nil, tool.Error("source.seq must be an integer")
		}
		src.Seq = &seq
	}
	return src, nil
}

func write(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	id, _, err := tool.IntArg(args, "id", false)
	if err != nil {
		return nil, err
	}
	title, _, err := tool.TextArg(args, "title", false)
	if err != nil {
		return nil, err
	}
	body, _, err := tool.TextArg(args, "body", false)
	if err != nil {
		return nil, err
	}
	if title != "" && len([]rune(title)) > 200 {
		return nil, tool.Error("title is limited to 200 characters")
	}
	if err := writeBound.ClampText(ctx, tc.DB.Read, body, "body"); err != nil {
		return nil, err
	}
	scope, _, err := tool.TextArg(args, "scope", false)
	if err != nil {
		return nil, err
	}
	aliases, err := aliasesArg(args)
	if err != nil {
		return nil, err
	}
	source, err := sourceArg(args)
	if err != nil {
		return nil, err
	}
	if tc.Kind == tool.Nightly {
		if source == nil || !slices.Contains(tc.Sources, source.ThreadID) {
			return nil, tool.Error("source.thread_id must be one of the threads gathered for this run")
		}
	}
	rec, writeID, err := kb.Write(ctx, tc.Ledger, kb.Writer{
		ThreadID: tc.ThreadID, RunID: tc.RunID, Kind: string(tc.Kind), Now: tc.Now(),
	}, kb.WriteInput{ID: id, Title: title, Body: body, Aliases: aliases, Scope: scope, Source: source})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": rec.ID, "cite": kb.Cite(rec.ID), "write_id": writeID}, nil
}

func retract(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	id, ok, err := tool.IntArg(args, "id", true)
	if err != nil || !ok {
		return nil, tool.Error("id is required")
	}
	reason, ok, err := tool.TextArg(args, "reason", true)
	if err != nil || !ok {
		return nil, tool.Error("reason is required")
	}
	if len([]rune(reason)) > 200 {
		return nil, tool.Error("reason is limited to 200 characters")
	}
	if tc.Kind == tool.Nightly {
		rec, err := kb.Get(ctx, tc.DB.Read, id)
		if err != nil {
			return nil, err
		}
		if rec == nil || rec.SourceThreadID == nil || !slices.Contains(tc.Sources, *rec.SourceThreadID) {
			return nil, tool.Error("only records from the threads gathered for this run can be retracted")
		}
	}
	rec, writeID, err := kb.Retract(ctx, tc.Ledger, kb.Writer{
		ThreadID: tc.ThreadID, RunID: tc.RunID, Kind: string(tc.Kind), Now: tc.Now(),
	}, id, reason)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": rec.ID, "cite": kb.Cite(rec.ID), "retracted": true, "write_id": writeID}, nil
}

const searchSchema = `{"type": "object", "properties": {"query": {"type": "string", "maxLength": 200}, ` +
	`"scope": {"enum": ["work", "personal"]}, "limit": {"type": "integer", "minimum": 1, "maximum": 20}}, ` +
	`"required": ["query"], "additionalProperties": false}`

const getSchema = `{"type": "object", "properties": {"ids": {"type": "array", "items": {"type": "integer"}, ` +
	`"minItems": 1, "maxItems": 10}}, "required": ["ids"], "additionalProperties": false}`

const retractSchema = `{"type": "object", "properties": {"id": {"type": "integer"}, ` +
	`"reason": {"type": "string", "maxLength": 200}}, "required": ["id", "reason"], "additionalProperties": false}`

func writeSchema() string {
	return fmt.Sprintf(`{"type": "object", "properties": {"id": {"type": "integer"}, `+
		`"title": {"type": "string", "maxLength": 200}, "body": %s, `+
		`"aliases": {"type": "array", "items": {"type": "string", "maxLength": 80}, "maxItems": 10}, `+
		`"scope": {"enum": ["work", "personal"]}, `+
		`"source": {"type": "object", "properties": {"thread_id": {"type": "integer"}, "seq": {"type": "integer"}}, "required": ["thread_id"], "additionalProperties": false}}, `+
		`"additionalProperties": false}`, writeBound.TextSchema())
}

func Tools() []tool.Tool {
	return []tool.Tool{
		{
			Name: "kb_search",
			Description: "Search the operator's knowledge base of durable facts, decisions and references. " +
				"Use it before writing anything durable, and cite results as [K<id>]. Results marked untrusted, pii or messages carry those marks into this thread.",
			InputSchema: json.RawMessage(searchSchema),
			Handler:     search,
			Effect:      tool.Read,
			Annotated:   true,
			Marks:       tool.AllMarks,
			Contexts:    tool.AllKinds,
		},
		{
			Name:        "kb_get",
			Description: "Read full knowledge records by id, including retracted ones. Cite as [K<id>].",
			InputSchema: json.RawMessage(getSchema),
			Handler:     get,
			Effect:      tool.Read,
			Annotated:   true,
			Marks:       tool.AllMarks,
			Contexts:    tool.AllKinds,
		},
		{
			Name: "kb_write",
			Description: "Create or edit one durable knowledge record: a fact, decision or reference worth keeping. " +
				"Creating needs title and body; pass id to edit an unconfirmed record. Never store secrets. Logged and undoable by the operator.",
			InputSchema: json.RawMessage(writeSchema()),
			Handler:     write,
			Effect:      tool.WriteUndo, Undo: kb.UndoOp,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  tool.DefaultKinds,
		},
		{
			Name:        "kb_retract",
			Description: "Retract a knowledge record that is wrong or superseded. The record stays in history and the operator can undo. Give a short reason.",
			InputSchema: json.RawMessage(retractSchema),
			Handler:     retract,
			Effect:      tool.WriteUndo, Undo: kb.UndoOp,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  tool.DefaultKinds,
		},
	}
}
