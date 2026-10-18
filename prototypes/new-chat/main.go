// PROTOTYPE, throwaway: renders the static new-chat page states for jeiang/atrium#44.
// All numbers are fake. Run via ./run.sh.
package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type ThinkOpt struct {
	ID, Label, Note string
	MaxEffort       string // "" = any effort
}

type Model struct {
	ID, Name      string
	In, Read, W1h float64 // $/MTok (fake)
	DefaultEffort string
	DefaultCtx    int
	Efforts       []string
	Thinking      []ThinkOpt
	TierCliff     int // tokens; above this the whole request bills at TierMult
	TierMult      float64
}

var efforts = []string{"low", "medium", "high", "max"}

var models = []Model{
	{ID: "haiku", Name: "Haiku 5.5", In: 1, Read: 0.10, W1h: 2, DefaultEffort: "medium", DefaultCtx: 90000, Efforts: efforts,
		Thinking: []ThinkOpt{{"adaptive", "Adaptive", "", ""}, {"disabled", "Off", "only at effort high or below", "high"}}, TierCliff: 100000, TierMult: 2},
	{ID: "sonnet", Name: "Sonnet 5.5", In: 3, Read: 0.30, W1h: 6, DefaultEffort: "medium", DefaultCtx: 200000, Efforts: efforts,
		Thinking: []ThinkOpt{{"adaptive", "Adaptive", "", ""}, {"between_tools", "Between tools", "only at effort low, medium or high", "high"}}},
	{ID: "opus", Name: "Opus 5.5", In: 5, Read: 0.50, W1h: 10, DefaultEffort: "high", DefaultCtx: 200000, Efforts: efforts,
		Thinking: []ThinkOpt{{"adaptive", "Adaptive", "always on for Opus", ""}}},
}

var ctxSizes = []int{90000, 200000, 500000, 1000000}

func byID(id string) Model {
	for _, m := range models {
		if m.ID == id {
			return m
		}
	}
	panic(id)
}

func mult(m Model, tokens int) float64 {
	if m.TierCliff > 0 && tokens > m.TierCliff {
		return m.TierMult
	}
	return 1
}

// per-step cache-read cost with the context full
func readCost(m Model, tokens int) float64 {
	return float64(tokens) / 1e6 * m.Read * mult(m, tokens)
}
func writeCost(m Model, tokens int) float64 {
	return float64(tokens) / 1e6 * m.W1h * mult(m, tokens)
}

func fmtK(t int) string {
	if t >= 1000000 {
		return fmt.Sprintf("%dM", t/1000000)
	}
	return fmt.Sprintf("%dK", t/1000)
}

func money(f float64) string {
	if f < 0.1 {
		return fmt.Sprintf("$%.3f", f)
	}
	return fmt.Sprintf("$%.2f", f)
}

func valid(m Model, think, effort string) bool {
	for _, t := range m.Thinking {
		if t.ID == think {
			return t.MaxEffort == "" || effortRank(effort) <= effortRank(t.MaxEffort)
		}
	}
	return false
}

func effortRank(e string) int {
	for i, x := range efforts {
		if x == e {
			return i
		}
	}
	return 0
}

type Form struct {
	Prompt                       string
	Model, Effort, Think         string
	Ctx                          int
	Coding                       bool
	Env, Repo, Branch            string
	Agent, AgentAuth             string
	Project                      string
	AdvancedOpen                 bool
	WebSearch                    bool
	CostCap                      string
}

type Thread struct {
	Title, Model, Effort, Think string
	Ctx, Cached                 int
	WarmMin                     int // 0 = cold
	TTLMin                      int
	Project, Env, Repo, Branch  string
	WorkBranch, Agent           string
	SwitchTo                    string // model id previewed in the picker ("" = none)
	MsgEffort                   string // per-message effort override ("" = base)
	Panel                       string // settings | cost
	Slug                        string
}

type Page struct {
	File, Title, Heading, Note string
	Models                     []Model
	CtxSizes                   []int
	Efforts                    []string
	Form                       *Form
	Thread                     *Thread
	States                     []State
	Current                    string
}

type State struct{ File, Name, Desc string }

func (t Thread) CurModel() Model { return byID(t.Model) }
func (t Thread) Target() Model {
	if t.SwitchTo == "" {
		return t.CurModel()
	}
	return byID(t.SwitchTo)
}
func (t Thread) Switching() bool { return t.SwitchTo != "" && t.SwitchTo != t.Model }
func (t Thread) Warm() bool      { return t.WarmMin > 0 }
func (t Thread) Down() bool {
	return t.Switching() && rank(t.Target().ID) < rank(t.Model)
}
func rank(id string) int {
	for i, m := range models {
		if m.ID == id {
			return i
		}
	}
	return 0
}
func (t Thread) CurStep() float64 { return readCost(t.CurModel(), t.Cached) }
func (t Thread) NewStep() float64 { return readCost(t.Target(), t.Cached) }
func (t Thread) Rewrite() float64 { return writeCost(t.Target(), t.Cached) }
func (t Thread) ThinkLabel() string {
	for _, o := range t.CurModel().Thinking {
		if o.ID == t.Think {
			return o.Label
		}
	}
	return t.Think
}
func (t Thread) ShownEffort() string {
	if t.MsgEffort != "" {
		return t.MsgEffort
	}
	return t.Effort
}

var iconRe = regexp.MustCompile(`(?m)^  "([a-z0-9-]+)": '(.*)',?$`)

func loadIcons(path string) map[string]string {
	b, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	out := map[string]string{}
	for _, m := range iconRe.FindAllStringSubmatch(string(b), -1) {
		out[m[1]] = m[2]
	}
	return out
}

func main() {
	icons := loadIcons("../../src/atrium/web/templates/_icons.html")
	funcs := template.FuncMap{
		"icon": func(name string, class ...string) template.HTML {
			c := ""
			if len(class) > 0 {
				c = class[0]
			}
			return template.HTML(fmt.Sprintf(`<svg class="lucide lucide-%s %s" xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">%s</svg>`, name, c, icons[name]))
		},
		"money":    money,
		"fmtK":     fmtK,
		"readCost": readCost,
		"writeCost": writeCost,
		"model":    byID,
		"json": func(v any) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
		"valid":   valid,
		"upper":   strings.ToUpper,
		"mul":     func(a float64, b float64) float64 { return a * b },
		"pct":     func(a, b float64) string { return fmt.Sprintf("%.0f%%", a/b*100) },
		"selAttr": func(ok bool) template.HTMLAttr { if ok { return "selected" }; return "" },
		"chkAttr": func(ok bool) template.HTMLAttr { if ok { return "checked" }; return "" },
		"stem":    func(s string) string { return strings.TrimSuffix(s, ".html") },
		"eq2":     func(a, b string) bool { return a == b },
	}

	states := []State{
		{"index.html", "Index", ""},
		{"new-fresh.html", "1 Fresh chat", "Assistant chat, default controls (Sonnet 5.5, medium)."},
		{"new-coding.html", "2 Coding thread", "Environment, repo, branch and coding-agent pickers."},
		{"new-haiku-cliff.html", "3 Haiku cliff", "Haiku selected with 200K context: tier cliff note beside the control."},
		{"thread-switch-warm.html", "4 Switch model, warm", "Cache warm: switching rewrites the cache, cost line shown."},
		{"thread-switch-cold.html", "5 Switch model, cold", "Cache cold: switching is free; down-switch drops thinking."},
		{"thread-effort.html", "6 Per-message effort", "Composer effort chip changed for one message."},
		{"thread-fixed.html", "7 Fixed fields", "Existing thread: creation-time fields read-only."},
		{"thread-cost.html", "8 Cost / cache panel", "Thread cost, cache hit share, savings, warm timer."},
	}

	coding := &Form{Prompt: "", Model: "sonnet", Effort: "medium", Think: "adaptive", Ctx: 200000, Coding: true,
		Env: "go-web", Repo: "jeiang/atrium", Branch: "main", Agent: "atrium", AgentAuth: "api", Project: "atrium v2", AdvancedOpen: true}
	warm := Thread{Title: "Fix flaky scheduler test", Model: "sonnet", Effort: "medium", Think: "adaptive", Ctx: 200000, Cached: 142000,
		WarmMin: 41, TTLMin: 60, Project: "atrium v2", Env: "go-web", Repo: "jeiang/atrium", Branch: "main", WorkBranch: "atrium/fix-flaky-scheduler-test", Agent: "Claude Code (plan login)", Slug: "warm"}
	cold := warm
	cold.Model, cold.Think, cold.Effort, cold.WarmMin, cold.SwitchTo, cold.Slug = "opus", "adaptive", "high", 0, "sonnet", "cold"

	pages := []Page{
		{File: "index.html", Title: "Prototype index", Heading: "New-chat prototype"},
		{File: "new-fresh.html", Title: "New chat", Heading: "New chat", Form: &Form{Model: "sonnet", Effort: "medium", Think: "adaptive", Ctx: 200000, Project: "None"}},
		{File: "new-coding.html", Title: "New chat · coding", Heading: "New chat", Form: coding},
		{File: "new-haiku-cliff.html", Title: "New chat · Haiku", Heading: "New chat", Form: &Form{Model: "haiku", Effort: "medium", Think: "adaptive", Ctx: 200000, Project: "None", AdvancedOpen: true}},
		{File: "thread-switch-warm.html", Title: warm.Title, Heading: warm.Title, Thread: func() *Thread { t := warm; t.SwitchTo = "opus"; t.Panel = "settings"; return &t }()},
		{File: "thread-switch-cold.html", Title: cold.Title, Heading: cold.Title, Thread: func() *Thread { t := cold; t.Panel = "settings"; return &t }()},
		{File: "thread-effort.html", Title: warm.Title, Heading: warm.Title, Thread: func() *Thread { t := warm; t.MsgEffort = "high"; t.Panel = "settings"; return &t }()},
		{File: "thread-fixed.html", Title: warm.Title, Heading: warm.Title, Thread: func() *Thread { t := warm; t.Panel = "settings"; return &t }()},
		{File: "thread-cost.html", Title: warm.Title, Heading: warm.Title, Thread: func() *Thread { t := warm; t.Panel = "cost"; return &t }()},
	}

	tdir := "tmpl"
	layout := filepath.Join(tdir, "layout.html")
	parts, _ := filepath.Glob(filepath.Join(tdir, "_*.html"))
	for i := range pages {
		p := &pages[i]
		p.Models, p.CtxSizes, p.Efforts, p.States, p.Current = models, ctxSizes, efforts, states, p.File
		name := "new.html"
		switch {
		case p.File == "index.html":
			name = "index.html"
		case p.Thread != nil:
			name = "thread.html"
		}
		files := append([]string{layout, filepath.Join(tdir, name)}, parts...)
		t := template.Must(template.New("layout.html").Funcs(funcs).ParseFiles(files...))
		f, err := os.Create(filepath.Join("out", p.File))
		if err != nil {
			panic(err)
		}
		if err := t.ExecuteTemplate(f, "layout.html", p); err != nil {
			panic(err)
		}
		f.Close()
		if p.Thread != nil { // htmx fragments for the side-panel tabs
			os.MkdirAll("out/frag", 0o755)
			for _, panel := range []string{"settings", "cost"} {
				tt := *p.Thread
				tt.Panel = panel
				pp := *p
				pp.Thread = &tt
				ff, _ := os.Create(filepath.Join("out/frag", strings.TrimSuffix(p.File, ".html")+"-"+panel+".html"))
				if err := t.ExecuteTemplate(ff, "side", pp); err != nil {
					panic(err)
				}
				ff.Close()
			}
		}
	}
}
