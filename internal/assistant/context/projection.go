package context

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/py"
)

type Message struct {
	Role    string
	Content []json.RawMessage
	Seq     *int64
	RunID   *int64
}

func (m Message) Wire() json.RawMessage {
	var b strings.Builder
	b.WriteString(`{"role":`)
	b.WriteString(py.JSONDumps(m.Role, compactJSON))
	b.WriteString(`,"content":[`)
	for i, raw := range m.Content {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(raw)
	}
	b.WriteString("]}")
	return json.RawMessage(b.String())
}

func SetField(raw json.RawMessage, key string, value any) (json.RawMessage, error) {
	d, err := blockDict(raw)
	if err != nil {
		return nil, err
	}
	return encode(d.Set(key, value)), nil
}

func TextBlock(text string) json.RawMessage {
	return encode(py.Dict{{Key: "type", Value: Text}, {Key: "text", Value: text}})
}

func blockDict(raw json.RawMessage) (py.Dict, error) {
	v, err := py.JSONLoads(raw)
	if err != nil {
		return nil, fmt.Errorf("context: content block: %w", err)
	}
	d, ok := v.(py.Dict)
	if !ok {
		return nil, fmt.Errorf("context: content block is %s, not an object", py.TypeName(v))
	}
	return d, nil
}
