package tpltest

import (
	"bytes"
	"encoding/json"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

// Call is one macro call of a macro-library golden.
type Call struct {
	Macro  string         `json:"macro"`
	Args   []any          `json:"args"`
	Kwargs map[string]any `json:"kwargs"`
	Caller *string        `json:"caller"`
}

// Case is a golden context file: the Jinja environment globals, the render context and the macro calls.
type Case struct {
	Globals map[string]any `json:"globals"`
	Context map[string]any `json:"context"`
	Calls   []Call         `json:"calls"`
}

// Want is the golden render of name ("thread.html", "basecoat/toast.html").
func Want(t testing.TB, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(golden.Dir(), "html", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Load reads the context file next to a golden render ("thread.context.json"). Numbers are int64 or float64,
// and {"$safe": "<html>"} objects are template.HTML.
func Load(t testing.TB, name string) Case {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(golden.Dir(), "html", name))
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Globals json.RawMessage `json:"globals"`
		Context json.RawMessage `json:"context"`
		Calls   []struct {
			Macro  string          `json:"macro"`
			Args   json.RawMessage `json:"args"`
			Kwargs json.RawMessage `json:"kwargs"`
			Caller *string         `json:"caller"`
		} `json:"calls"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	decode := func(data json.RawMessage) any {
		if len(data) == 0 {
			return nil
		}
		v, err := decodeOrdered(json.NewDecoder(bytes.NewReader(data)))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	c := Case{Globals: map[string]any{}, Context: map[string]any{}}
	if m, ok := decode(raw.Globals).(map[string]any); ok {
		c.Globals = m
	}
	if m, ok := decode(raw.Context).(map[string]any); ok {
		c.Context = m
	}
	for _, call := range raw.Calls {
		out := Call{Macro: call.Macro, Caller: call.Caller, Args: []any{}, Kwargs: map[string]any{}}
		if a, ok := decode(call.Args).([]any); ok {
			out.Args = a
		}
		if m, ok := decode(call.Kwargs).(map[string]any); ok {
			out.Kwargs = m
		}
		c.Calls = append(c.Calls, out)
	}
	return c
}

func decodeOrdered(dec *json.Decoder) (any, error) {
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch x := tok.(type) {
	case json.Delim:
		if x == '[' {
			out := []any{}
			for dec.More() {
				e, err := decodeOrdered(dec)
				if err != nil {
					return nil, err
				}
				out = append(out, e)
			}
			_, err := dec.Token()
			return out, err
		}
		var pairs py.Dict
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return nil, err
			}
			e, err := decodeOrdered(dec)
			if err != nil {
				return nil, err
			}
			pairs = append(pairs, py.Pair{Key: key.(string), Value: e})
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		if len(pairs) == 1 && pairs[0].Key == "$safe" {
			if safe, ok := pairs[0].Value.(string); ok {
				return template.HTML(safe), nil
			}
		}
		return orderedIfNeeded(pairs), nil
	case json.Number:
		if strings.ContainsAny(string(x), ".eE") {
			f, _ := x.Float64()
			return f, nil
		}
		i, _ := x.Int64()
		return i, nil
	}
	return tok, nil
}

func orderedIfNeeded(pairs py.Dict) map[string]any {
	return py.OrderedMap(pairs)
}
