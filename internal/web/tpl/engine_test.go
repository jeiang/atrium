package tpl

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"testing/fstest"
)

const hostile = `<script>alert("x")</script>' & "q" javascript:alert(1)`

func hostileEngine(t *testing.T) *Engine {
	t.Helper()
	extra := fstest.MapFS{
		"hostile.gohtml": {Data: []byte(`{{define "content"}}
{{template "sidebar" (dict "id" .Data.text "label" .Data.text "header" .Data.text "footer" .Data.text "menu" (list
	(dict "type" "group" "label" .Data.text "items" (list
		(dict "label" .Data.text "url" .Data.url "icon" .Data.text "current" true "class" .Data.text)))
	(dict "type" "submenu" "label" .Data.text "open" true "items" (list (dict "label" .Data.text "url" .Data.url)))))}}
{{template "toaster" (dict "toasts" (list (dict "category" .Data.text "title" .Data.text "description" .Data.text "icon" .Data.text
	"action" (dict "label" .Data.text "href" .Data.url) "cancel" (dict "label" .Data.text "onclick" .Data.text))))}}
{{template "tabs" (dict "id" "t" "label" .Data.text "variant" .Data.text "class" .Data.text "tabsets" (list (dict "tab" .Data.text "panel" .Data.text)))}}
{{template "external" .Data.url}} {{template "preview_attrs" .Data.url}} {{template "open_attrs" .Data.text}}
<a {{template "external" .Data.url}}>x</a><a {{template "preview_attrs" .Data.url}}>x</a>
{{template "mode_badge" (dict "mode" .Data.text)}}
{{template "pending_badge" (dict "count" .Data.text)}}
{{template "empty_state" (dict "icon_name" "search" "title" .Data.text "body" .Data.text)}}
{{template "spinner" (dict "id" .Data.text "label" .Data.text)}}
{{template "page_link" (dict "ref" (dict "id" .Data.text "title" .Data.text) "class" .Data.text)}}
{{template "account_switch" (dict "path" .Data.url "current" .Data.text "label" .Data.text)}}
{{template "chat_item" (dict "csrf" .Data.text "thread" (dict "id" .Data.text "kind" "chat") "last_seq" 1 "item" (dict "kind" "assistant" "seq" 2 "text" .Data.markdown
	"citations" (list (dict "url" .Data.url "title" .Data.text) (dict "title" .Data.text "cited" .Data.text))))}}
{{template "chat_item" (dict "thread" (dict "id" 1 "kind" "chat") "item" (dict "kind" "user" "seq" 1 "text" .Data.text "files" (list (dict "id" 1 "name" .Data.text "media_type" .Data.text))))}}
{{template "activity_step" (dict "kind" "web_search" "results" (list (dict "url" .Data.url "title" .Data.text)) "error" .Data.text)}}
{{template "condition_text" (dict "field" .Data.text "op" .Data.text "value" (list .Data.text))}}
{{icon "x" .Data.text}}{{.Data.json}}
{{end}}`)},
	}
	e, err := New(Config{AssetVersion: "v", Globals: map[string]any{"accounts": []string{hostile}}, Extra: extra})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func hostileFrame() *Frame {
	return &Frame{
		CSRF: hostile, Mode: "live", Paused: true, PendingCount: 3, Page: hostile,
		Toast: &Toast{Category: hostile, Title: hostile, Description: hostile},
		Data: map[string]any{
			"text":     hostile,
			"url":      "javascript:alert(document.cookie)",
			"markdown": "[x](javascript:alert(1)) <script>alert(1)</script> ![i](https://e.com/a.png) <img src=x onerror=alert(1)>",
			"json":     template.JS(`alert(1)`),
		},
	}
}

func TestHostileDataIsEscapedInContext(t *testing.T) {
	e := hostileEngine(t)
	var out bytes.Buffer
	if err := e.Page(&out, "hostile.gohtml", hostileFrame()); err != nil {
		t.Fatal(err)
	}
	page := out.String()
	for _, bad := range []string{"ZgotmplZ", "<script>alert", `href="javascript`, `hx-get="javascript`, "<img"} {
		if strings.Contains(page, bad) {
			t.Errorf("rendered page contains %q", bad)
		}
	}
	for _, want := range []string{
		`hx-headers='{&#34;X-CSRF-Token&#34;: &#34;\u003cscript\u003ealert(\&#34;x\&#34;)\u003c/script\u003e\u0027 \u0026 \&#34;q\&#34; javascript:alert(1)&#34;}'`,
		`&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;&#39; &amp; &#34;q&#34; javascript:alert(1)`,
		`href="#"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("rendered page misses %q", want)
		}
	}
}

func TestHostileFragmentsHaveNoZgotmplZ(t *testing.T) {
	e := hostileEngine(t)
	var out bytes.Buffer
	if err := e.Fragment(&out, "_toast.gohtml", hostileFrame()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "ZgotmplZ") || strings.Contains(out.String(), "<script>") {
		t.Errorf("toast fragment: %s", out.String())
	}
	out.Reset()
	f := hostileFrame()
	f.Data = ErrorView{Status: 422, Problem: hostile, Path: "//evil.example/<x>"}
	if err := e.Page(&out, "error.gohtml", f); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "ZgotmplZ") || strings.Contains(out.String(), "<script>alert") || strings.Contains(out.String(), "evil.example") {
		t.Errorf("error page: %s", out.String())
	}
}

func TestTrustedTypesCrossTheBoundaryOnlyWhenTyped(t *testing.T) {
	e := hostileEngine(t)
	f := hostileFrame()
	f.Data.(map[string]any)["text"] = template.HTML("<b>trusted</b>")
	var out bytes.Buffer
	if err := e.Page(&out, "hostile.gohtml", f); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "<b>trusted</b>") {
		t.Error("template.HTML did not render as markup in a trusted slot")
	}
	f = hostileFrame()
	out.Reset()
	if err := e.Page(&out, "hostile.gohtml", f); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "<b>") {
		t.Error("a plain string rendered as markup")
	}
}

func TestMissingKeyIsAnError(t *testing.T) {
	extra := fstest.MapFS{
		"typo.gohtml":   {Data: []byte(`{{define "content"}}{{.Data.propsal}}{{end}}`)},
		"fields.gohtml": {Data: []byte(`{{define "content"}}{{.Pagee}}{{end}}`)},
		"deep.gohtml":   {Data: []byte(`{{define "content"}}{{.Data.thread.titel}}{{end}}`)},
	}
	e, err := New(Config{Extra: extra})
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{"proposal": 1, "thread": map[string]any{"title": "x"}}
	for _, page := range []string{"typo.gohtml", "fields.gohtml", "deep.gohtml"} {
		var out bytes.Buffer
		err := e.Page(&out, page, &Frame{Data: data})
		if err == nil {
			t.Errorf("%s rendered %q instead of failing", page, out.String())
			continue
		}
		if out.Len() != 0 {
			t.Errorf("%s wrote partial output before failing", page)
		}
	}
	var out bytes.Buffer
	err = e.Page(&out, "typo.gohtml", &Frame{Data: data})
	if err == nil || !strings.Contains(err.Error(), `map has no entry for key "propsal"`) {
		t.Errorf("error = %v", err)
	}
}

func TestUnknownIconAndTemplateFail(t *testing.T) {
	extra := fstest.MapFS{
		"icon.gohtml":    {Data: []byte(`{{define "content"}}{{icon "no-such"}}{{end}}`)},
		"missing.gohtml": {Data: []byte(`{{define "content"}}{{template "no_such_macro" .}}{{end}}`)},
	}
	if _, err := New(Config{Extra: extra}); err != nil {
		t.Fatal(err)
	}
	e, _ := New(Config{Extra: extra})
	for _, page := range []string{"icon.gohtml", "missing.gohtml"} {
		if err := e.Page(&bytes.Buffer{}, page, &Frame{}); err == nil {
			t.Errorf("%s did not fail", page)
		}
	}
	if err := e.Page(&bytes.Buffer{}, "nope.gohtml", &Frame{}); err == nil {
		t.Error("unknown page rendered")
	}
	if err := e.Fragment(&bytes.Buffer{}, "base.gohtml", &Frame{}); err == nil {
		t.Error("layout rendered as a fragment")
	}
}

func TestAssetURL(t *testing.T) {
	if got, want := AssetURL("abc123", "basecoat/all.min.js"), "/static/basecoat/all.min.js?v=abc123"; got != want {
		t.Errorf("AssetURL = %s, want %s", got, want)
	}
	e, err := New(Config{AssetVersion: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := e.Page(&out, "error.gohtml", &Frame{Data: ErrorView{Status: 404}}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`href="/static/app.css?v=abc123"`, `src="/static/htmx.min.js?v=abc123"`, `src="/static/basecoat/all.min.js?v=abc123"`, `src="/static/app.js?v=abc123"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("layout misses %s", want)
		}
	}
}

func TestPartialPageSetsDoNotLeak(t *testing.T) {
	extra := fstest.MapFS{
		"one.gohtml": {Data: []byte(`{{define "title"}}one{{end}}{{define "content"}}1{{end}}`)},
		"two.gohtml": {Data: []byte(`{{define "title"}}two{{end}}{{define "content"}}2{{end}}`)},
	}
	e, err := New(Config{Extra: extra})
	if err != nil {
		t.Fatal(err)
	}
	for page, want := range map[string]string{"one.gohtml": "<title>one · atrium</title>", "two.gohtml": "<title>two · atrium</title>"} {
		var out bytes.Buffer
		if err := e.Page(&out, page, &Frame{}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), want) {
			t.Errorf("%s: %s", page, out.String())
		}
	}
}
