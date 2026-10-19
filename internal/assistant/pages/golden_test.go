package pages

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/web/tpl"
	"github.com/jeiang/atrium/internal/web/tpl/tpltest"
)

func goldenEngine(t testing.TB, c tpltest.Case) *tpl.Engine {
	t.Helper()
	version, _ := c.Globals["asset_version"].(string)
	e, err := tpl.New(tpl.Config{AssetVersion: version, Globals: c.Globals, Modules: []fs.FS{Templates()}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func goldenFrame(c tpltest.Case, page string) *tpl.Frame {
	f := &tpl.Frame{Data: c.Context, Page: page}
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
		description, _ := m["description"].(string)
		f.Toast = &tpl.Toast{Category: m["category"].(string), Title: m["title"].(string), Description: description}
	}
	return f
}

const sandboxWhy = "#134 nit 10: the code sandbox UI is not ported in Phase A"

var (
	dropSandboxToggle = tpltest.Rewrite{Drop: true, Why: sandboxWhy, Only: func(token string) bool {
		return strings.HasPrefix(token, "<label ") && strings.Contains(token, `title="Let the assistant run code in Anthropic's sandbox and produce files"`)
	}}
	dropSandboxNote = tpltest.Rewrite{Drop: true, Why: sandboxWhy, Only: func(token string) bool {
		return strings.HasPrefix(token, `<p id="sandbox" `)
	}}
	dropCodeSessions = tpltest.Rewrite{Drop: true, Why: "#134 nit 10: workspace.view returns code: [] and the Code sessions section is not ported", Only: func(token string) bool {
		return strings.HasPrefix(token, "<section ") && strings.Contains(token, `aria-labelledby="work-code"`)
	}}
	macroRewrites = []tpltest.Rewrite{
		{From: `rel="noopener noreferrer"`, To: `rel="noreferrer"`, Why: "#121: bluemonday RequireNoReferrerOnLinks emits only noreferrer; nh3 emitted both for markdown links", Only: func(token string) bool { return strings.HasPrefix(token, "<a href=") }},
		{From: `javascript:alert(document.cookie)`, To: `#`, Why: "#121: external() filters schemes outside http, https, mailto, tel and notion"},
		{From: `[bad](javascript:alert(1))`, To: `bad`, Why: "#121: markdown-it leaves a javascript: link as literal text; goldmark parses the link and bluemonday strips it to its label"},
	}
	composerRewrites  = append([]tpltest.Rewrite{dropSandboxToggle, dropSandboxNote}, macroRewrites...)
	workspaceRewrites = append([]tpltest.Rewrite{dropSandboxToggle, dropSandboxNote, dropCodeSessions}, macroRewrites...)
	workRewrites      = append([]tpltest.Rewrite{dropCodeSessions}, macroRewrites...)
)

var pageGoldens = map[string][]tpltest.Rewrite{
	"chat":           nil,
	"chat@archived":  nil,
	"thread":         workspaceRewrites,
	"thread@running": workspaceRewrites,
}

var fragmentGoldens = map[string][]tpltest.Rewrite{
	"_thread_list": nil, "_thread_list@pinned-only": nil, "_thread_list@empty": nil, "_thread_list@archived-empty": nil,
	"_thread_list@search-empty": nil, "_thread_list@project-empty": nil,
	"_thread": composerRewrites, "_thread@running": composerRewrites, "_thread@subagent": nil,
	"_transcript": macroRewrites, "_transcript@running": macroRewrites, "_transcript@empty": nil,
	"_run_live": nil, "_run_started": nil, "_run_done": macroRewrites, "_run_done@done": macroRewrites, "_run_done@interrupted": macroRewrites,
	"_queued": nil, "_meter": nil, "_meter@low": nil,
	"_work": workRewrites, "_work@empty": workRewrites, "_work_update": workRewrites,
	"_file": macroRewrites, "_file@image": nil, "_file@none": nil, "_file@table": nil, "_file@text": nil,
	"_project_files": nil, "_project_files@archived": nil,
}

var unported = []string{"_sandbox", "_sandbox@off"}

func TestGoldenPages(t *testing.T) {
	for name, rewrites := range pageGoldens {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			var out bytes.Buffer
			if err := goldenEngine(t, c).Page(&out, strings.SplitN(name, "@", 2)[0]+".gohtml", goldenFrame(c, "chat")); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String(), rewrites...)
		})
	}
}

func TestGoldenFragments(t *testing.T) {
	for name, rewrites := range fragmentGoldens {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			var out bytes.Buffer
			if err := goldenEngine(t, c).Fragment(&out, strings.SplitN(name, "@", 2)[0]+".gohtml", goldenFrame(c, "")); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String(), rewrites...)
		})
	}
	for _, name := range unported {
		t.Run(name, func(t *testing.T) { t.Skip(sandboxWhy) })
	}
}
