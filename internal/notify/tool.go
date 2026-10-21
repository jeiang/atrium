package notify

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/py"
)

const description = "Send the operator a short plain-text notification (phone push plus the Notifications page). " +
	"Each run has a small notification limit (smaller in the nightly run: its summary) and the tool reports when it is reached; " +
	"use only for things that need the operator's attention. " +
	"thread_link is a thread id or /chat/<id>; defaults to this thread."

var inputSchema = json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},` +
	`"body":{"type":"string"},"thread_link":{"type":"string"}},"required":["title","body"],"additionalProperties":false}`)

func Tools() []tool.Tool {
	return []tool.Tool{{
		Name:        "notify",
		Description: description,
		InputSchema: inputSchema,
		Handler:     handle,
		Effect:      tool.Notify,
		Contexts:    tool.DefaultKinds,
	}}
}

func handle(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	title, _, err := tool.TextArg(args, "title", true)
	if err != nil {
		return nil, err
	}
	body, _, err := tool.TextArg(args, "body", true)
	if err != nil {
		return nil, err
	}
	var used int
	if err := tc.DB.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM notifications WHERE run_id = ?", nullID(tc.RunID)).Scan(&used); err != nil {
		return nil, err
	}
	capKey := KeyPerRunCap
	if tc.Kind == tool.Nightly {
		capKey = KeyNightlyCap
	}
	limit, err := whole(ctx, tc.DB.Read, capKey)
	if err != nil {
		return nil, err
	}
	if used >= limit {
		plural := "s"
		if limit == 1 {
			plural = ""
		}
		return nil, tool.Error(fmt.Sprintf("at most %d notification%s per run", limit, plural))
	}
	m := Message{Title: title, Body: body, ThreadID: tc.ThreadID, RunID: tc.RunID, Now: tc.Now}
	if link, ok, err := tool.TextArg(args, "thread_link", false); err != nil {
		return nil, err
	} else if ok {
		m.ThreadLink = &link
	}
	sent, err := Send(ctx, tc.DB.Write, tc.Env, m)
	if err != nil {
		return nil, err
	}
	var note any
	if sent.Error != "" {
		note = sent.Error
	}
	return py.Dict{{Key: "recorded", Value: true}, {Key: "delivered", Value: sent.Delivered}, {Key: "note", Value: note}}, nil
}
