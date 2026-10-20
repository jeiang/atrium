package loop

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/jeiang/atrium/internal/py"
)

type blockField struct {
	name   string
	models []string
}

type blockModel struct {
	types  []string
	fields []blockField
}

func (m blockModel) declares(key string) bool {
	return slices.ContainsFunc(m.fields, func(f blockField) bool { return f.name == key })
}

var journalJSON = py.JSONOpts{Compact: true, NoEnsureASCII: true}

func journalBlock(raw json.RawMessage) (json.RawMessage, error) {
	v, err := py.JSONLoads(raw)
	if err != nil {
		return nil, fmt.Errorf("loop: response block: %w", err)
	}
	return json.RawMessage(py.JSONDumps(modelDump(v, blockRoots), journalJSON)), nil
}

func journalBlocks(content []json.RawMessage) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, len(content))
	for i, raw := range content {
		b, err := journalBlock(raw)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

func modelDump(v any, candidates []string) any {
	if len(candidates) == 0 {
		return v
	}
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = modelDump(e, candidates)
		}
		return out
	case py.Dict:
		m, ok := modelOf(x, candidates)
		if !ok {
			return x
		}
		out := make(py.Dict, 0, len(x))
		for _, f := range m.fields {
			if value, ok := x.Get(f.name); ok {
				out = append(out, py.Pair{Key: f.name, Value: modelDump(value, f.models)})
			}
		}
		for _, p := range x {
			if !m.declares(p.Key) {
				out = append(out, p)
			}
		}
		return out
	}
	return v
}

func modelOf(d py.Dict, candidates []string) (blockModel, bool) {
	if len(candidates) == 1 {
		return blockModels[candidates[0]], true
	}
	kind, _ := d.Get("type")
	name, _ := kind.(string)
	for _, c := range candidates {
		if m := blockModels[c]; slices.Contains(m.types, name) {
			return m, true
		}
	}
	return blockModel{}, false
}
