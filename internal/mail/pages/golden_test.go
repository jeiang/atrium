package pages

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/web/tpl"
	"github.com/jeiang/atrium/internal/web/tpl/tpltest"
)

var pageGoldens = []string{
	"message", "message@chat", "triage", "triage@shadow", "rules", "rules@account", "rule_edit", "rule_edit@new",
}

var fragmentGoldens = []string{
	"_message",
	"_decision_card", "_decision_card@none", "_decision_card@shadow-correction", "_decision_card@stage-correction",
	"_decision_card@stage-fallback", "_decision_card@stage-knn", "_decision_card@stage-learned", "_decision_card@stage-llm",
	"_decision_card@stage-operator",
	"_correct", "_correct@message",
	"_decisions", "_triage_rows", "_triage_rows@empty", "_triage_rows@empty-filtered", "_triage_row",
	"_rule_list", "_rule_list@empty", "_rule_form", "_rule_form@new",
	"_rule_preview", "_rule_preview@all", "_rule_preview@none", "_rule_preview@error",
	"_condition_row", "_condition_row@valueless",
}

var rewrites = map[string][]tpltest.Rewrite{
	"_decision_card@shadow-correction": {{
		From: "Recorded by you as a correction.",
		To:   "Recorded by you as a correction. Shadow mode leaves the message where it is.",
		Why:  "the Jinja macro reads the environment mode, not the render-context override the golden sets; the app never overrides mode, so in shadow the sentence shows",
		Only: func(token string) bool { return strings.HasPrefix(token, `text "Recorded by you`) },
	}},
}

func goldenFrame(c tpltest.Case) *tpl.Frame {
	f := &tpl.Frame{Data: c.Context}
	f.CSRF, _ = c.Globals["csrf_token"].(string)
	f.Mode, _ = c.Globals["mode"].(string)
	if v, ok := c.Context["mode"].(string); ok {
		f.Mode = v
	}
	f.Paused, _ = c.Context["paused"].(bool)
	if n, ok := c.Context["pending_count"].(int64); ok {
		f.PendingCount = int(n)
	}
	if m, ok := c.Context["toast"].(map[string]any); ok {
		f.Toast = &tpl.Toast{Category: m["category"].(string), Title: m["title"].(string), Description: m["description"].(string)}
	}
	return f
}

func goldenEngine(t testing.TB, c tpltest.Case) *tpl.Engine {
	t.Helper()
	globals := map[string]any{}
	for k, v := range c.Globals {
		globals[k] = v
	}
	version, _ := c.Globals["asset_version"].(string)
	e, err := tpl.New(tpl.Config{AssetVersion: version, Globals: globals, Modules: []fs.FS{Templates()}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestGoldenPages(t *testing.T) {
	for _, name := range pageGoldens {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			var out bytes.Buffer
			if err := goldenEngine(t, c).Page(&out, strings.SplitN(name, "@", 2)[0]+".gohtml", goldenFrame(c)); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String(), rewrites[name]...)
		})
	}
}

func TestGoldenFragments(t *testing.T) {
	for _, name := range fragmentGoldens {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			var out bytes.Buffer
			if err := goldenEngine(t, c).Fragment(&out, strings.SplitN(name, "@", 2)[0]+".gohtml", goldenFrame(c)); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String(), rewrites[name]...)
		})
	}
}
