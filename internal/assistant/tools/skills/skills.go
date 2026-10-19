package skillstools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/skills"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/py"
)

const Name = "load_skill"

func load(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	name, ok, err := tool.TextArg(args, "name", true)
	if err != nil {
		return nil, err
	}
	shown := "None"
	var found *skills.Skill
	if ok {
		shown = py.Repr(name)
		if found, err = skills.Get(ctx, tc.DB.Read, name); err != nil {
			return nil, err
		}
	}
	if found == nil {
		return nil, tool.Error("no skill named " + shown)
	}
	loaded, err := skills.LoadedInThread(ctx, tc.DB.Read, tc.ThreadID)
	if err != nil {
		return nil, err
	}
	loaded[name] = true
	related, unknown, err := skills.RelatedTo(ctx, tc.DB.Read, name, loaded)
	if err != nil {
		return nil, err
	}
	body := found.Body
	references := []string{}
	if len(related) > 0 {
		lines := make([]string, len(related))
		for i, r := range related {
			lines[i] = "- " + r.Name + ": " + r.Description
			references = append(references, r.Name)
		}
		body += "\n\nReferenced skills not yet loaded in this thread (call load_skill if they apply):\n" + strings.Join(lines, "\n")
	}
	if len(unknown) > 0 {
		body += "\n\nReferenced skills that do not exist: " + strings.Join(unknown, ", ")
	}
	return py.Dict{
		{Key: "name", Value: found.Name},
		{Key: "description", Value: found.Description},
		{Key: "body", Value: body},
		{Key: "references", Value: references},
	}, nil
}

func Tools() []tool.Tool {
	return []tool.Tool{{
		Name:        Name,
		Description: "Read an operator-written skill playbook by name (names are listed in the system prompt). Skills may reference others as /skill:name; the result lists referenced skills you have not loaded yet.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"], "additionalProperties": false}`),
		Handler:     load,
		Effect:      tool.Read,
		Contexts:    tool.Kinds{tool.Chat, tool.Cron, tool.Nightly},
	}}
}
