package registry

import (
	"bytes"
	"encoding/json"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/settings"
)

const DynamicFilteringKey = "dynamic_filtering"

func init() {
	settings.MustRegister(settings.Value(settings.Advanced, settings.Def{
		Key: DynamicFilteringKey, Module: "web_search", Type: settings.Bool, Default: "false",
	}))
}

const (
	WebSearchName = "web_search"
	webSearchType = "web_search_20260318"
)

func webSearchTool() tool.Tool {
	return tool.Tool{
		Name:        WebSearchName,
		Description: "Anthropic web search (server tool); pair it with web_fetch to read results.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Effect:      tool.Read,
		Annotated:   true,
		Flags:       tool.Flags{Web: true, UntrustedOutput: true, ExternalEffect: true},
		Contexts:    tool.Kinds{tool.Chat, tool.Cron, tool.Sub},
		Server:      true,
	}
}

type toolParam struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

type serverParam struct {
	Type           string          `json:"type"`
	Name           string          `json:"name"`
	MaxUses        int             `json:"max_uses"`
	AllowedCallers []string        `json:"allowed_callers,omitempty"`
	CacheControl   json.RawMessage `json:"cache_control,omitempty"`
}

func (s Set) Params(webSearchMaxUses int, cacheControl json.RawMessage, dynamicFiltering bool) ([]json.RawMessage, error) {
	items := make([]any, 0, len(s.Dispatch)+len(s.Server))
	for _, t := range s.Dispatch {
		items = append(items, toolParam{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	var callers []string
	if !dynamicFiltering {
		callers = []string{"direct"}
	}
	for _, t := range s.Server {
		items = append(items, serverParam{Type: webSearchType, Name: t.Name, MaxUses: webSearchMaxUses, AllowedCallers: callers})
	}
	if len(items) == 0 {
		return nil, nil
	}
	switch last := items[len(items)-1].(type) {
	case toolParam:
		last.CacheControl = cacheControl
		items[len(items)-1] = last
	case serverParam:
		last.CacheControl = cacheControl
		items[len(items)-1] = last
	}
	out := make([]json.RawMessage, len(items))
	for i, item := range items {
		raw, err := encode(item)
		if err != nil {
			return nil, err
		}
		out[i] = raw
	}
	return out, nil
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
