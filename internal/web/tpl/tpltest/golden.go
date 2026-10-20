package tpltest

import (
	"bytes"
	"encoding/json"
	"html/template"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

type Call struct {
	Macro  string         `json:"macro"`
	Args   []any          `json:"args"`
	Kwargs map[string]any `json:"kwargs"`
	Caller *string        `json:"caller"`
}

type Case struct {
	Globals map[string]any `json:"globals"`
	Context map[string]any `json:"context"`
	Calls   []Call         `json:"calls"`

	orders map[string][]string
}

func (c Case) Ordered(t testing.TB, path string) py.Dict {
	t.Helper()
	var node any = c.Context
	for _, part := range strings.Split(path, ".") {
		switch x := node.(type) {
		case map[string]any:
			node = x[part]
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				t.Fatalf("tpltest: bad index %q in %q", part, path)
			}
			node = x[i]
		default:
			t.Fatalf("tpltest: %q does not lead to an object", path)
		}
	}
	m, ok := node.(map[string]any)
	if !ok {
		t.Fatalf("tpltest: %q is %T, not an object", path, node)
	}
	out := make(py.Dict, 0, len(m))
	for _, k := range c.orders[path] {
		out = append(out, py.Pair{Key: k, Value: m[k]})
	}
	return out
}

func Want(t testing.TB, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(golden.Dir(), "html", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

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
	c := Case{Globals: map[string]any{}, Context: map[string]any{}, orders: map[string][]string{}}
	decode := func(data json.RawMessage, path string, orders map[string][]string) any {
		if len(data) == 0 {
			return nil
		}
		v, err := decodeValue(json.NewDecoder(bytes.NewReader(data)), path, orders)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if m, ok := decode(raw.Globals, "", map[string][]string{}).(map[string]any); ok {
		c.Globals = m
	}
	if m, ok := decode(raw.Context, "", c.orders).(map[string]any); ok {
		c.Context = m
	}
	for _, call := range raw.Calls {
		out := Call{Macro: call.Macro, Caller: call.Caller, Args: []any{}, Kwargs: map[string]any{}}
		if a, ok := decode(call.Args, "", map[string][]string{}).([]any); ok {
			out.Args = a
		}
		if m, ok := decode(call.Kwargs, "", map[string][]string{}).(map[string]any); ok {
			out.Kwargs = m
		}
		c.Calls = append(c.Calls, out)
	}
	return c
}

func join(path, part string) string {
	if path == "" {
		return part
	}
	return path + "." + part
}

func decodeValue(dec *json.Decoder, path string, orders map[string][]string) (any, error) {
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
				e, err := decodeValue(dec, join(path, strconv.Itoa(len(out))), orders)
				if err != nil {
					return nil, err
				}
				out = append(out, e)
			}
			_, err := dec.Token()
			return out, err
		}
		out := map[string]any{}
		var keys []string
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return nil, err
			}
			name := key.(string)
			e, err := decodeValue(dec, join(path, name), orders)
			if err != nil {
				return nil, err
			}
			if _, seen := out[name]; !seen {
				keys = append(keys, name)
			}
			out[name] = e
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		if safe, ok := out["$safe"].(string); ok && len(out) == 1 {
			return template.HTML(safe), nil
		}
		orders[path] = keys
		return out, nil
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
