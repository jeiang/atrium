package notifytools

import (
	"context"
	"encoding/json"

	modules "github.com/jeiang/atrium/internal/module"
	notifyapp "github.com/jeiang/atrium/internal/notify/application"
	"github.com/jeiang/atrium/internal/tool"
)

const description = "Send the operator a short plain-text notification (phone push plus the Notifications page). " +
	"Each run has a small notification limit (smaller in the nightly run: its summary) and the tool reports when it is reached; " +
	"use only for things that need the operator's attention. " +
	"thread_link is a thread id or /chat/<id>; defaults to this thread."

var inputSchema = json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},` +
	`"body":{"type":"string"},"thread_link":{"type":"string"}},"required":["title","body"],"additionalProperties":false}`)

func Metadata() tool.Tool {
	return tool.Tool{
		Name:        "notify",
		Description: description,
		InputSchema: inputSchema,
		Effect:      tool.Notify,
		Annotated:   true,
		Channel:     tool.ChannelNotify,
		Contexts:    tool.DefaultKinds,
	}
}

func Definition(factory func(*tool.Context) *notifyapp.Service) modules.ToolDefinition {
	t := Metadata()
	t.Handler = func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		return handle(ctx, tc, args, factory(tc))
	}
	return modules.ToolDefinition{Tool: t}
}

func handle(ctx context.Context, tc *tool.Context, args tool.Args, service *notifyapp.Service) (any, error) {
	title, _, err := tool.TextArg(args, "title", true)
	if err != nil {
		return nil, err
	}
	body, _, err := tool.TextArg(args, "body", true)
	if err != nil {
		return nil, err
	}
	m := notifyapp.Message{Title: title, Body: body, ThreadID: tc.ThreadID, RunID: tc.RunID, Now: tc.Now}
	if tc.ProposalID != nil {
		m.ProposalID = *tc.ProposalID
	}
	if link, ok, err := tool.TextArg(args, "thread_link", false); err != nil {
		return nil, err
	} else if ok {
		m.ThreadLink = &link
	}
	change, err := service.Send(ctx, notifyapp.SendInput{Message: m, RunKind: &tc.Kind})
	sent := change.Data
	if err != nil {
		return nil, err
	}
	var note any
	if sent.Error != "" {
		note = sent.Error
	}
	return map[string]any{"recorded": true, "delivered": sent.Delivered, "note": note}, nil
}
