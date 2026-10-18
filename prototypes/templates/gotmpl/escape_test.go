package main

// PROTOTYPE (#75): escaping experiments + fragment rendering + smoke tests of macros the two pages don't reach.
// Run: go test ./gotmpl -run . -v   (results are t.Logf'd; assertions only where a behaviour is load-bearing).

import (
	"bytes"
	"encoding/json"
	"html/template"
	"os"
	"strings"
	"testing"
	"time"
)

func exec(t *testing.T, src string, data any) string {
	t.Helper()
	tpl, err := template.New("x").Funcs(funcMap(func() *template.Template { return nil })).Parse(src)
	if err != nil {
		return "PARSE ERROR: " + err.Error()
	}
	var b bytes.Buffer
	if err := tpl.Execute(&b, data); err != nil {
		return "EXEC ERROR: " + err.Error()
	}
	return b.String()
}

func TestEscapingContexts(t *testing.T) {
	cases := []struct {
		name, src string
		data      any
	}{
		{"href javascript:", `<a href="{{.}}">x</a>`, "javascript:alert(1)"},
		{"href javascript: as template.URL", `<a href="{{.}}">x</a>`, template.URL("javascript:alert(1)")},
		{"href with space/unicode/&", `<a href="/a/{{.}}">x</a>`, "a b/é&c?d#e"},
		{"query value, auto-encoded", `<a href="/x?account={{.}}">x</a>`, "a b&c=d"},
		{"query value, explicit urlencode (double-encode)", `<a href="/x?account={{urlencode .}}">x</a>`, "a b&c=d"},
		{"path segment, normalizer keeps ?/#", `<a href="/notion/{{.}}">x</a>`, "a/b?c#d"},
		{"path segment + urlencode", `<a href="/notion/{{urlencode .}}">x</a>`, "a/b?c#d"},
		{"style int", `<i style="width: {{.}}%">`, int64(82)},
		{"style float", `<i style="width: {{.}}%">`, 82.3},
		{"style numeric string", `<i style="width: {{.}}%">`, "82"},
		{"style hostile string", `<i style="width: {{.}}%">`, "1px;background:url(//evil)"},
		{"style calc()", `<i style="width: {{.}}">`, "calc(100% - 2rem)"},
		{"style whole decl as string", `<i style="{{.}}">`, "width: 82%"},
		{"style whole decl as template.CSS", `<i style="{{.}}">`, template.CSS("width: 82%")},
		{"onclick string (Jinja: raw text)", `<button onclick="{{.}}">`, "doThing(1)"},
		{"onclick template.JS", `<button onclick="{{.}}">`, template.JS("doThing(1)")},
		{"hx-vals single-quoted, string json", `<b hx-vals='{{.}}'>`, `{"forever": "1"}`},
		{"hx-vals, template.JS json", `<b hx-vals='{{.}}'>`, template.JS(`{"forever": "1"}`)},
		{"hx-headers tojson", `<b hx-headers='{{tojson .}}'>`, map[string]any{"X-CSRF-Token": "a'b\"c<"}},
		{"data-* attr", `<b data-x="{{.}}">`, `a"b'c<d`},
		{"hx-get (url-ish attr, not filtered)", `<b hx-get="{{.}}">`, "javascript:alert(1)"},
		{"unquoted attr", `<b title={{.}}>`, "a b"},
		{"template.HTML (icon) inside attribute", `<b title="{{.}}">`, template.HTML(`<svg class="x"><path d="M1"/></svg>t`)},
		{"template.HTML in text", `<b>{{.}}</b>`, template.HTML(`<svg><path d="M1"/></svg>`)},
		{"plain string with markup in text", `<b>{{.}}</b>`, `<svg>&"'`},
		{"quote spelling", `<b>{{.}}</b>`, `"'<>&+`},
		{"nil value", `<b>[{{.x}}]</b>`, map[string]any{}},
		{"missing key typo", `<b>[{{.tpyo}}]</b>`, map[string]any{"typo": 1}},
		{"missing nested key", `<b>[{{.a.b}}]</b>`, map[string]any{}},
		{"eq nil vs string", `<b>{{if eq .x "live"}}y{{else}}n{{end}}</b>`, map[string]any{}},
		{"eq int64 vs int", `<b>{{if eq .x 7}}y{{else}}n{{end}}</b>`, map[string]any{"x": int64(7)}},
		{"eq float vs int", `<b>{{if eq .x 7}}y{{else}}n{{end}}</b>`, map[string]any{"x": 7.0}},
		{"float64 integral printed", `<b>{{.}}</b>`, 1000000.0},
		{"dynamic attr name class", `<b {{range $k, $v := .}}{{$k}}="{{$v}}" {{end}}>`, map[string]any{"class": "a b"}},
		{"dynamic attr name href", `<b {{range $k, $v := .}}{{$k}}="{{$v}}" {{end}}>`, map[string]any{"href": "/x"}},
		{"dynamic attr name style", `<b {{range $k, $v := .}}{{$k}}="{{$v}}" {{end}}>`, map[string]any{"style": "color:red"}},
		{"dynamic attr name onclick", `<b {{range $k, $v := .}}{{$k}}="{{$v}}" {{end}}>`, map[string]any{"onclick": "x()"}},
		{"dynamic attr name hx-get", `<b {{range $k, $v := .}}{{$k}}="{{$v}}" {{end}}>`, map[string]any{"hx-get": "/x"}},
		{"dynamic attr name weird", `<b {{range $k, $v := .}}{{$k}}="{{$v}}" {{end}}>`, map[string]any{"[&>span]:x": "1"}},
		{"dynamic attr name as HTMLAttr", `<b {{.}}>`, template.HTMLAttr(`onclick="x()"`)},
		{"branches ending in different contexts", `<b {{if .}}title="x"{{end}}>`, true},
		{"unbalanced tag in if", `{{if .}}<a href="/x">{{end}}t`, true},
		{"undefined template in untaken branch", `{{if .}}{{template "nope" .}}{{end}}ok`, false},
		{"branches ending in different contexts (real)", `<b {{if .}}title="x{{end}}>`, true},
		{"comment in template", "<p>a<!-- secret -->b</p>", nil},
		{"template call inside tag", `{{define "ext"}}href="{{.}}" target="_blank"{{end}}<a {{template "ext" .}}>x</a>`, "javascript:alert(1)"},
		{"template call inside tag (ok url)", `{{define "ext"}}href="{{.}}" target="_blank"{{end}}<a {{template "ext" .}}>x</a>`, "https://a.b/?q=1&r=2"},
	}
	for _, c := range cases {
		t.Logf("%-52s -> %s", c.name, exec(t, c.src, c.data))
	}
}

func loadData(t *testing.T) map[string]any {
	raw, err := os.ReadFile("../data.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var data map[string]any
	if err := dec.Decode(&data); err != nil {
		t.Fatal(err)
	}
	normalise(data)
	return data
}

// htmx story: render one named template (a "fragment") from a page set. This is what an hx-post handler would do.
func TestFragments(t *testing.T) {
	data := loadData(t)
	ctx := data["proposals"].(map[string]any)
	root, err := build("proposals")
	if err != nil {
		t.Fatal(err)
	}
	p := ctx["proposals"].([]any)[0]
	var b bytes.Buffer
	frag := func(name string, d any) string {
		b.Reset()
		if err := root.ExecuteTemplate(&b, name, d); err != nil {
			return "ERROR: " + err.Error()
		}
		return b.String()
	}
	out := frag("_proposal.gohtml", withKV(ctx, "proposal", p))
	t.Logf("_proposal.gohtml fragment: %d bytes, starts %q", len(out), strings.TrimSpace(out)[:60])
	t.Logf("status_badge macro alone: %s", frag("status_badge", "applied"))
	t.Logf("pending_badge oob alone: %s", frag("pending_badge", map[string]any{"count": int64(4), "oob": true}))
	t.Logf("unknown fragment: %s", frag("nope", nil))
	t.Logf("fragment with wrong data (proposal missing): %s", frag("_proposal.gohtml", ctx))
	t.Logf("fragment with typo'd data key (propsal): %s", frag("_proposal.gohtml", withKV(ctx, "propsal", p)))
}

func withKV(m map[string]any, kv ...any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	for i := 0; i < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

// Macros/templates the two fake-data pages never reach: execute them so html/template's escaper (which is lazy,
// per entry-point) checks them, and look at the risky ones.
func TestUnreachedMacros(t *testing.T) {
	root, err := build("proposals")
	if err != nil {
		t.Fatal(err)
	}
	run := func(name string, d any) string {
		var b bytes.Buffer
		if err := root.ExecuteTemplate(&b, name, d); err != nil {
			return "ERROR: " + err.Error()
		}
		return strings.Join(strings.Fields(b.String()), " ")
	}
	one := int64(1)
	for _, c := range []struct {
		n string
		d any
	}{
		{"mode_note", map[string]any{"mode": "shadow"}},
		{"action_badge", map[string]any{"status": "applied", "error": "x\"y"}},
		{"stage_badge", "knn"},
		{"reason", map[string]any{"mode": "shadow", "d": map[string]any{"stage": "learned", "key": "addr", "confidence": 0.5, "detail": map[string]any{"n": one}}}},
		{"place", nil},
		{"place", "A|B"},
		{"proposal", map[string]any{"action": "move", "dest": "X"}},
		{"agreement", "agree"},
		{"account_switch", map[string]any{"path": "/triage", "current": "gmail"}},
		{"folder_options", map[string]any{"folders": []any{map[string]any{"folder": "A&B"}}, "selected": "A&B"}},
		{"condition_text", map[string]any{"field": "subject", "op": "in", "value": []any{"a", "b"}}},
		{"condition_text", map[string]any{"field": "age_days", "op": "gte", "value": int64(3)}},
		{"run_badge", "running"},
		{"run_badge", "done"},
		{"page_path", []any{map[string]any{"title": "A"}, map[string]any{"title": "B"}}},
		{"spinner", map[string]any{"id": "sp"}},
		{"toast", map[string]any{"category": "info", "title": "T", "action": map[string]any{"label": "Go", "onclick": "doThing(1)"}}},
		{"toast", map[string]any{"category": "info", "title": "T", "action": map[string]any{"label": "Go", "onclick": template.JS("doThing(1)")}}},
		{"toast", map[string]any{"category": "info", "action": map[string]any{"label": "Go", "href": "javascript:alert(1)"}, "cancel": map[string]any{"label": "No", "url": "/x"}}},
		{"toast", map[string]any{"category": "warning", "duration": int64(-1), "attrs": map[string]any{"class": "c", "id": "t1", "style": "x:y", "hx-on:click": "a()"}}},
		{"toast", map[string]any{"icon": template.HTML("<i>ico</i>"), "title": "T"}},
		{"toast", map[string]any{"icon": "<i>ico</i>", "title": "T"}},
		{"toaster", map[string]any{"toasts": []any{map[string]any{"title": "A"}}, "caller": template.HTML("<b>caller</b>")}},
		{"sidebar", map[string]any{"id": "s", "menu": []any{
			map[string]any{"type": "separator"},
			map[string]any{"type": "submenu", "label": "Sub", "open": true, "items": []any{map[string]any{"label": "a & b", "url": "javascript:alert(1)"}}},
			map[string]any{"label": "<i>x</i>", "url": "/x"}}}},
		{"sidebar", map[string]any{"id": "s", "caller": template.HTML("<p>caller</p>"), "open": false, "side": "right", "main_attrs": map[string]any{"class": "z", "data-a": "1"}}},
	} {
		t.Logf("%s(%v) => %s", c.n, c.d, run(c.n, c.d))
	}
}

func TestTiming(t *testing.T) {
	data := loadData(t)
	t0 := time.Now()
	root, _ := build("proposals")
	t.Logf("build(proposals) (parse 15 files, no escaping yet): %v", time.Since(t0))
	var b bytes.Buffer
	t0 = time.Now()
	_ = root.ExecuteTemplate(&b, "base.gohtml", data["proposals"])
	t.Logf("first execute (includes lazy escaping): %v, %d bytes", time.Since(t0), b.Len())
	b.Reset()
	t0 = time.Now()
	for range 100 {
		b.Reset()
		_ = root.ExecuteTemplate(&b, "base.gohtml", data["proposals"])
	}
	t.Logf("steady-state execute x100 avg: %v", time.Since(t0)/100)
}
