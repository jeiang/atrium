package block

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/py"
)

const (
	Text             = "text"
	ToolUse          = "tool_use"
	ToolResult       = "tool_result"
	Thinking         = "thinking"
	RedactedThinking = "redacted_thinking"
	Attachment       = "attachment"
	Compaction       = "compaction"
	ServerToolUse    = "server_tool_use"
	WebSearchResult  = "web_search_tool_result"
	Fallback         = "fallback"
)

type Block struct {
	Kind  string
	Value py.Dict
	Raw   json.RawMessage
}

func Decode(raw json.RawMessage) (Block, error) {
	v, err := py.JSONLoads(raw)
	if err != nil {
		return Block{}, fmt.Errorf("block: %w", err)
	}
	d, ok := v.(py.Dict)
	if !ok {
		return Block{}, fmt.Errorf("block: not an object")
	}
	kind, _ := d.Get("type")
	name, _ := kind.(string)
	return Block{Kind: name, Value: d, Raw: raw}, nil
}

func DecodeAll(content []json.RawMessage) ([]Block, error) {
	out := make([]Block, len(content))
	for i, raw := range content {
		b, err := Decode(raw)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

func (b Block) Get(key string) any {
	v, _ := b.Value.Get(key)
	return v
}

func (b Block) String(key string) string {
	s, _ := b.Get(key).(string)
	return s
}

func (b Block) Text() string { return b.String("text") }

func (b Block) VisibleText() bool { return strings.TrimSpace(b.Text()) != "" }

func HasKind(content []json.RawMessage, kind string) (bool, error) {
	blocks, err := DecodeAll(content)
	if err != nil {
		return false, err
	}
	for _, b := range blocks {
		if b.Kind == kind {
			return true, nil
		}
	}
	return false, nil
}

func HasText(content []json.RawMessage) (bool, error) {
	return HasKind(content, Text)
}

func VisibleText(content []json.RawMessage) (bool, error) {
	blocks, err := DecodeAll(content)
	if err != nil {
		return false, err
	}
	for _, b := range blocks {
		if b.Kind == Text && b.VisibleText() {
			return true, nil
		}
	}
	return false, nil
}

func Texts(content []json.RawMessage) ([]string, error) {
	blocks, err := DecodeAll(content)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, b := range blocks {
		if b.Kind == Text {
			out = append(out, b.Text())
		}
	}
	return out, nil
}

func WithoutToolUses(content []json.RawMessage) ([]json.RawMessage, error) {
	blocks, err := DecodeAll(content)
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(content))
	for _, b := range blocks {
		if b.Kind != ToolUse {
			out = append(out, b.Raw)
		}
	}
	return out, nil
}

func SearchURLs(content []json.RawMessage) ([]string, error) {
	blocks, err := DecodeAll(content)
	if err != nil {
		return nil, err
	}
	var urls []string
	for _, b := range blocks {
		if b.Kind != WebSearchResult {
			continue
		}
		list, ok := b.Get("content").([]any)
		if !ok {
			continue
		}
		for _, raw := range list {
			d, ok := raw.(py.Dict)
			if !ok {
				continue
			}
			if u, ok := d.Get("url"); ok {
				if s, ok := u.(string); ok && s != "" {
					urls = append(urls, s)
				}
			}
		}
	}
	return urls, nil
}

type Use struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func (u Use) Args() (json.RawMessage, any, error) {
	if len(u.Input) == 0 {
		return json.RawMessage(`{}`), py.Dict{}, nil
	}
	v, err := py.JSONLoads(u.Input)
	if err != nil {
		return nil, nil, fmt.Errorf("block: tool %s input: %w", u.Name, err)
	}
	if !py.Truthy(v) {
		return json.RawMessage(`{}`), py.Dict{}, nil
	}
	return u.Input, v, nil
}

func ToolUses(content []json.RawMessage) ([]Use, error) {
	blocks, err := DecodeAll(content)
	if err != nil {
		return nil, err
	}
	var uses []Use
	for _, b := range blocks {
		if b.Kind != ToolUse {
			continue
		}
		var u Use
		if err := json.Unmarshal(b.Raw, &u); err != nil {
			return nil, fmt.Errorf("block: tool_use: %w", err)
		}
		uses = append(uses, u)
	}
	return uses, nil
}
