package registry

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/jeiang/atrium/internal/assistant/tool"
)

const (
	WebSearchName = "web_search"
	webSearchType = "web_search_20260318"
)

var WebSearchKinds = tool.Kinds{tool.Chat, tool.Cron}

var WebSearchFlags = tool.Annotated | tool.UntrustedOutput | tool.ExternalEffect

type WebSearch struct {
	Enabled          bool
	MaxUses          int
	DynamicFiltering bool
}

func (w WebSearch) param() json.RawMessage {
	callers := `,"allowed_callers":["direct"]`
	if w.DynamicFiltering {
		callers = ""
	}
	return json.RawMessage(`{"type":"` + webSearchType + `","name":"` + WebSearchName + `","max_uses":` + strconv.Itoa(w.MaxUses) + callers + `}`)
}

type toolParam struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

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

func ServerTools(kind tool.Kind, allow []string, web WebSearch) []json.RawMessage {
	if web.Enabled && (WebSearchKinds.Has(kind) || (kind == tool.Sub && allow != nil && slices.Contains(allow, WebSearchName))) {
		return []json.RawMessage{web.param()}
	}
	return nil
}

func RequestTools(tools []tool.Tool, kind tool.Kind, allow []string, cacheControl json.RawMessage, web WebSearch) ([]json.RawMessage, error) {
	params, err := Params(tools)
	if err != nil {
		return nil, err
	}
	params = append(params, ServerTools(kind, allow, web)...)
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
