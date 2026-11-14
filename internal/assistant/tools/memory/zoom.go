package memorytools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/tool"
)

const Name = "zoom"

const Description = "Open a memory line when its summary is not enough: a summary's two children, or one message's source."

const schema = `{"type":"object","properties":{"id":{"type":"integer","description":"Start message ordinal."},` +
	`"n":{"type":"integer","minimum":1,"description":"Covered messages; a power of two."},` +
	`"at":{"type":"integer","minimum":0,"description":"Character offset for a leaf page."}},` +
	`"required":["id"],"additionalProperties":false}`

type Service func(*tool.Context) *asstapp.Service

func Tools(svc Service) []tool.Tool {
	return append([]tool.Tool{{
		Name: Name, Description: Description, InputSchema: json.RawMessage(schema), Handler: zoom(svc),
		Effect: tool.Read, Marks: tool.AllMarks, Annotated: true,
		Contexts: tool.Kinds{tool.Chat, tool.Cron, tool.Nightly, tool.Sub},
	}}, searchTools(svc)...)
}

func fail(err error) error {
	var e *application.Error
	if errors.As(err, &e) {
		return tool.Error(e.Message)
	}
	return err
}

func zoom(svc Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		id, _, err := tool.IntArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		n, err := tool.BoundedInt(args, "n", 1, 1<<20, 1)
		if err != nil {
			return nil, err
		}
		at, err := tool.BoundedInt(args, "at", 0, 1_000_000_000, 0)
		if err != nil {
			return nil, err
		}
		got, err := svc(tc).MemoryZoom(ctx, asstapp.MemoryZoomInput{ThreadID: tc.ThreadID, ID: id, N: n, At: at})
		if err != nil {
			return nil, fail(err)
		}
		z := got.Data
		switch {
		case z.Children != nil:
			children := make([]any, len(z.Children))
			for i, c := range z.Children {
				children[i] = map[string]any{"id": c.ID, "n": c.N, "to": c.To, "state": c.State, "text": c.Text}
			}
			return tool.Marked{Value: map[string]any{"id": id, "n": n, "children": children}, Marks: z.Marks}, nil
		case z.Page == nil:
			return tool.Marked{Value: map[string]any{
				"id": id, "n": 1, "result_id": z.ResultID, "use": "recall",
				"text": fmt.Sprintf("tool result %d; page it with recall", z.ResultID),
			}, Marks: z.Marks}, nil
		}
		var next any
		if z.Page.Next != nil {
			next = *z.Page.Next
		}
		return tool.Marked{Value: map[string]any{"id": id, "n": 1, "offset": at, "total_chars": z.Page.Total, "text": z.Page.Text, "next_offset": next}, Marks: z.Marks}, nil
	}
}
