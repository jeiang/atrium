package tpltest

import (
	"bytes"
	"encoding/json"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var c Case
	if err := dec.Decode(&c); err != nil {
		t.Fatal(err)
	}
	c.Globals = Normalise(c.Globals).(map[string]any)
	c.Context = Normalise(c.Context).(map[string]any)
	for i := range c.Calls {
		c.Calls[i].Args = Normalise(c.Calls[i].Args).([]any)
		c.Calls[i].Kwargs = Normalise(c.Calls[i].Kwargs).(map[string]any)
	}
	return c
}

// Normalise turns decoded JSON into the value shapes the Python render saw: json.Number becomes int64 or
// float64 and {"$safe": "..."} becomes template.HTML.
func Normalise(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if safe, ok := x["$safe"].(string); ok && len(x) == 1 {
			return template.HTML(safe)
		}
		for k, e := range x {
			x[k] = Normalise(e)
		}
	case []any:
		for i, e := range x {
			x[i] = Normalise(e)
		}
	case json.Number:
		if strings.ContainsAny(string(x), ".eE") {
			f, _ := x.Float64()
			return f
		}
		i, _ := x.Int64()
		return i
	}
	return v
}
