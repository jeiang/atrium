package registry

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/jeiang/atrium/internal/assistant/tool"
)

const (
	WebSearchName = "web_search"
	webSearchType = "web_search_20260318"
)

// WebSearchKinds are the kinds that always get the server-side web_search tool.
var WebSearchKinds = tool.Kinds{tool.Chat, tool.Cron}

// webSearch is WEB_SEARCH; allowed_callers keeps the model from calling it through anything but itself.
var webSearch = json.RawMessage(`{"type":"` + webSearchType + `","name":"` + WebSearchName + `","max_uses":5,"allowed_callers":["direct"]}`)

type toolParam struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Params is anthropic_tool_params: [{name, description, input_schema}] in the order given.
func Params(tools []tool.Tool) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(tools))
	for _, t := range tools {
		raw, err := encode(toolParam{t.Name, t.Description, t.InputSchema})
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, nil
}

// ServerTools is server_tools_for: web_search for chat and cron, and for a subagent only when its allowlist
// names it; never for nightly.
func ServerTools(kind tool.Kind, allow []string) []json.RawMessage {
	if WebSearchKinds.Has(kind) || (kind == tool.Sub && allow != nil && slices.Contains(allow, WebSearchName)) {
		return []json.RawMessage{slices.Clone(webSearch)}
	}
	return nil
}

// RequestTools is the tools array of a request: the function tools, then the server tools, the last entry
// carrying the one tools cache breakpoint cacheControl. Nil when the run has no tool at all.
func RequestTools(tools []tool.Tool, kind tool.Kind, allow []string, cacheControl json.RawMessage) ([]json.RawMessage, error) {
	params, err := Params(tools)
	if err != nil {
		return nil, err
	}
	params = append(params, ServerTools(kind, allow)...)
	if len(params) == 0 {
		return nil, nil
	}
	last := len(params) - 1
	params[last] = withCacheControl(params[last], cacheControl)
	return params, nil
}

func withCacheControl(object, cacheControl json.RawMessage) json.RawMessage {
	body := bytes.TrimSuffix(bytes.TrimSpace(object), []byte("}"))
	out := make([]byte, 0, len(body)+len(cacheControl)+len(`,"cache_control":}`))
	out = append(out, body...)
	out = append(out, `,"cache_control":`...)
	out = append(out, cacheControl...)
	return append(out, '}')
}

func encode(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func isObject(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(raw, &m) == nil && m != nil
}
