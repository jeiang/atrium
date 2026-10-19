package pages

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/budget"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/skills"
	"github.com/jeiang/atrium/internal/assistant/store"
	"github.com/jeiang/atrium/internal/assistant/subagents"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/web/tpl/tpltest"
)

func gmap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func gmaps(v any) []map[string]any {
	list, _ := v.([]any)
	out := make([]map[string]any, len(list))
	for i, e := range list {
		out[i] = gmap(e)
	}
	return out
}

func gstr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func gopt(m map[string]any, key string) *string {
	if s, ok := m[key].(string); ok {
		return &s
	}
	return nil
}

func gfloat(m map[string]any, key string) float64 {
	if f := goptFloat(m, key); f != nil {
		return *f
	}
	return 0
}

func goptFloat(m map[string]any, key string) *float64 {
	switch n := m[key].(type) {
	case float64:
		return &n
	case int64:
		f := float64(n)
		return &f
	}
	return nil
}

func gint(m map[string]any, key string) int64 {
	if n := goptInt(m, key); n != nil {
		return *n
	}
	return 0
}

func goptInt(m map[string]any, key string) *int64 {
	if n, ok := m[key].(int64); ok {
		return &n
	}
	return nil
}

func gflag(m map[string]any, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case int64:
		return v != 0
	}
	return false
}

func gstrs(v any) []string {
	list, _ := v.([]any)
	out := make([]string, len(list))
	for i, e := range list {
		out[i], _ = e.(string)
	}
	return out
}

func gints(v any) []int64 {
	list, _ := v.([]any)
	out := make([]int64, len(list))
	for i, e := range list {
		out[i], _ = e.(int64)
	}
	return out
}

func goldenProject(m map[string]any) *projects.Project {
	if m == nil {
		return nil
	}
	return &projects.Project{
		ID: gint(m, "id"), Name: gstr(m, "name"), Instructions: gstr(m, "instructions"), Effort: gstr(m, "effort"), ContextLimit: gint(m, "context_limit"),
		WikiHubID: gopt(m, "wiki_hub_id"), Archived: store.Flag(gflag(m, "archived")), ThreadCount: gint(m, "thread_count"), LastActivity: goptFloat(m, "last_activity"),
	}
}

func goldenProjectList(v any) []projects.Project {
	out := []projects.Project{}
	for _, m := range gmaps(v) {
		out = append(out, *goldenProject(m))
	}
	return out
}

func goldenThreads(v any) []threads.Listed {
	out := []threads.Listed{}
	for _, m := range gmaps(v) {
		out = append(out, threads.Listed{
			ID: gint(m, "id"), Title: gopt(m, "title"), RunState: gopt(m, "run_state"), ProjectName: gopt(m, "project_name"), UpdatedAt: gfloat(m, "updated_at"),
			Cost: gfloat(m, "cost"), Pinned: store.Flag(gflag(m, "pinned")), Archived: store.Flag(gflag(m, "archived")),
		})
	}
	return out
}

func goldenFiles(v any) []files.Row {
	out := []files.Row{}
	for _, m := range gmaps(v) {
		out = append(out, files.Row{ID: gint(m, "id"), Name: gstr(m, "name"), MediaType: gstr(m, "media_type"), Bytes: gint(m, "bytes"), Tokens: gint(m, "tokens")})
	}
	return out
}

func goldenProjectValues(m map[string]any) ProjectFormValues {
	return ProjectFormValues{
		Name: gstr(m, "name"), Instructions: gstr(m, "instructions"), Effort: gstr(m, "effort"), ContextLimit: goptInt(m, "context_limit"), WikiHubID: gopt(m, "wiki_hub_id"),
	}
}

func goldenJobs(v any) []JobView {
	out := []JobView{}
	for _, m := range gmaps(v) {
		out = append(out, JobView{
			ID: gint(m, "id"), Name: gstr(m, "name"), Kind: gstr(m, "kind"), CreatedBy: gstr(m, "created_by"), Cadence: gstr(m, "cadence"),
			Schedule: gstr(m, "schedule"), Prompt: gstr(m, "prompt"), Enabled: gflag(m, "enabled"), NextDueText: gopt(m, "next_due_text"),
			LastStatus: gopt(m, "last_status"), LastError: gopt(m, "last_error"), LastStartedText: gopt(m, "last_started_text"),
		})
	}
	return out
}

func goldenRuns(v any) []RunView {
	out := []RunView{}
	for _, m := range gmaps(v) {
		out = append(out, RunView{
			JobName: gstr(m, "job_name"), Status: gstr(m, "status"), StartedText: gopt(m, "started_text"), Cost: gfloat(m, "cost"),
			Error: gopt(m, "error"), ThreadID: goptInt(m, "thread_id"),
		})
	}
	return out
}

func goldenSchedule(c tpltest.Case) any {
	return ScheduledData{Jobs: goldenJobs(c.Context["jobs"]), Runs: goldenRuns(c.Context["runs"]), TZ: gstr(c.Context, "tz")}
}

func goldenScheduleForm(c tpltest.Case) any {
	data := ScheduleFormData{Runs: goldenRuns(c.Context["runs"]), TZ: gstr(c.Context, "tz"), Error: gopt(c.Context, "error")}
	var job *scheduler.Job
	if m := gmap(c.Context["job"]); m != nil {
		job = &scheduler.Job{ID: gint(m, "id"), Name: gstr(m, "name"), Kind: gstr(m, "kind"), Schedule: gstr(m, "schedule"), Prompt: gstr(m, "prompt")}
		view := formJob(*job)
		data.Job = &view
	}
	var submitted *ScheduleFormValues
	if m := gmap(c.Context["values"]); m != nil {
		submitted = &ScheduleFormValues{Name: gstr(m, "name"), Schedule: gstr(m, "schedule"), Prompt: gstr(m, "prompt")}
	}
	data.Values = formValues(job, submitted)
	return data
}

func goldenRunList(c tpltest.Case) any {
	show := true
	if v, ok := c.Context["show_job"].(bool); ok {
		show = v
	}
	return map[string]any{"runs": goldenRuns(c.Context["runs"]), "show_job": show}
}

func goldenProjectsPage(c tpltest.Case) any {
	return ProjectsData{Projects: goldenProjectList(c.Context["projects"]), View: gstr(c.Context, "view")}
}

func goldenProjectPage(c tpltest.Case) any {
	return ProjectData{Project: goldenProject(gmap(c.Context["project"])), Files: goldenFiles(c.Context["files"]), Threads: goldenThreads(c.Context["threads"]), View: gstr(c.Context, "view")}
}

func goldenProjectForm(c tpltest.Case) any {
	return ProjectFormData{
		Project: goldenProject(gmap(c.Context["project"])), Values: goldenProjectValues(gmap(c.Context["values"])), Efforts: gstrs(c.Context["efforts"]),
		ContextLimits: gints(c.Context["context_limits"]), Error: gopt(c.Context, "error"),
	}
}

func goldenProjectHub(c tpltest.Case) any {
	return map[string]any{"project": goldenProject(gmap(c.Context["project"])), "hub_id": gstr(c.Context, "hub_id"), "notice": gopt(c.Context, "notice"), "error": gopt(c.Context, "error")}
}

func goldenAssistant(c tpltest.Case) any {
	defaults := gmap(c.Context["defaults"])
	return AssistantData{
		Defaults: threads.Options{Effort: gstr(defaults, "effort"), ContextLimit: gint(defaults, "context_limit")},
		Efforts:  gstrs(c.Context["efforts"]), ContextLimits: gints(c.Context["context_limits"]),
	}
}

func goldenUsage(c tpltest.Case) any {
	b, s := gmap(c.Context["budget"]), gmap(c.Context["summary"])
	data := UsageData{
		Budget: budget.Status{BudgetUSD: gfloat(b, "budget_usd"), SpentUSD: gfloat(b, "spent_usd"), RemainingUSD: gfloat(b, "remaining_usd"), Share: gfloat(b, "share")},
		Summary: usage.Summary{
			Calls: gint(s, "calls"), InputTokens: gint(s, "input_tokens"), CacheWrite: gint(s, "cache_write"), CacheRead: gint(s, "cache_read"),
			OutputTokens: gint(s, "output_tokens"), Searches: gint(s, "searches"), HighTier: gint(s, "high_tier"), CacheReadShare: gfloat(s, "cache_read_share"),
		},
		ByKind: []usage.KindRow{}, ByThread: []usage.ThreadRow{},
	}
	for _, m := range gmaps(c.Context["by_kind"]) {
		data.ByKind = append(data.ByKind, usage.KindRow{Kind: gstr(m, "kind"), Calls: gint(m, "calls"), Cost: gfloat(m, "cost"), Searches: gint(m, "searches")})
	}
	for _, m := range gmaps(c.Context["by_thread"]) {
		data.ByThread = append(data.ByThread, usage.ThreadRow{
			ThreadID: gint(m, "thread_id"), Title: gopt(m, "title"), Kind: gstr(m, "kind"), Calls: gint(m, "calls"), Cost: gfloat(m, "cost"),
			PromptTokens: gint(m, "prompt_tokens"), MaxPromptTokens: gint(m, "max_prompt_tokens"), OutputTokens: gint(m, "output_tokens"),
		})
	}
	return data
}

func goldenSkills(c tpltest.Case) any {
	data := SkillsData{Skills: []SkillView{}, Q: gstr(c.Context, "q")}
	for _, m := range gmaps(c.Context["skills"]) {
		refs := gmap(m["references"])
		data.Skills = append(data.Skills, skillView(skills.Listed{
			Skill:      skills.Skill{Name: gstr(m, "name"), Description: gstr(m, "description"), Body: gstr(m, "body")},
			References: skills.Refs{Known: gstrs(refs["known"]), Unknown: gstrs(refs["unknown"])}, ReferencedBy: gstrs(m["referenced_by"]),
		}))
	}
	draft := gmap(c.Context["draft"])
	data.Draft = SkillDraft{Name: gstr(draft, "name"), Description: gstr(draft, "description"), Body: gstr(draft, "body")}
	return data
}

func goldenSubagents(c tpltest.Case) any {
	data := SubagentsData{Agents: []AgentView{}, Pool: []subagents.PoolTool{}, Efforts: gstrs(c.Context["efforts"])}
	for _, m := range gmaps(c.Context["agents"]) {
		data.Agents = append(data.Agents, agentView(subagents.Agent{
			Name: gstr(m, "name"), Description: gstr(m, "description"), Prompt: gstr(m, "prompt"), Effort: gstr(m, "effort"),
			Enabled: gflag(m, "enabled"), Builtin: gflag(m, "builtin"), Tools: gstrs(m["tools"]),
		}))
	}
	for _, m := range gmaps(c.Context["pool"]) {
		data.Pool = append(data.Pool, subagents.PoolTool{Name: gstr(m, "name"), Description: gstr(m, "description")})
	}
	draft := gmap(c.Context["draft"])
	data.Draft = AgentView{Name: gstr(draft, "name"), Description: gstr(draft, "description"), Prompt: gstr(draft, "prompt"), Effort: gstr(draft, "effort"), Tools: gstrs(draft["tools"])}
	return data
}

const formactionWhy = "html/template normalizes URL attributes; the browser sends the same UTF-8 percent-encoded path for Jinja's raw characters"

var subagentFormaction = tpltest.Rewrite{
	From: "/settings/subagents/finance-Café naïve — 東京 🎉 مرحبا/",
	To:   "/settings/subagents/finance-Caf%c3%a9%20na%c3%afve%20%e2%80%94%20%e6%9d%b1%e4%ba%ac%20%f0%9f%8e%89%20%d9%85%d8%b1%d8%ad%d8%a8%d8%a7/",
	Why:  formactionWhy,
	Only: func(token string) bool {
		return strings.HasPrefix(token, "<button ") && strings.Contains(token, " formaction=")
	},
}

type assistantGolden struct {
	frame    string
	data     func(tpltest.Case) any
	rewrites []tpltest.Rewrite
}

var assistantPages = map[string]assistantGolden{
	"projects":                {"projects", goldenProjectsPage, nil},
	"projects@empty":          {"projects", goldenProjectsPage, nil},
	"projects@archived-empty": {"projects", goldenProjectsPage, nil},
	"project":                 {"projects", goldenProjectPage, nil},
	"project@archived":        {"projects", goldenProjectPage, nil},
	"scheduled":               {"scheduled", goldenSchedule, nil},
	"settings_assistant":      {"settings", goldenAssistant, nil},
	"usage":                   {"settings", goldenUsage, nil},
	"usage@empty":             {"settings", goldenUsage, nil},
	"skills":                  {"settings", goldenSkills, nil},
	"skills@empty":            {"settings", goldenSkills, nil},
	"skills@search-empty":     {"settings", goldenSkills, nil},
	"subagents":               {"settings", goldenSubagents, []tpltest.Rewrite{subagentFormaction}},
}

var assistantFragments = map[string]assistantGolden{
	"_project_form":         {"", goldenProjectForm, nil},
	"_project_form@new":     {"", goldenProjectForm, nil},
	"_project_hub":          {"", goldenProjectHub, nil},
	"_project_hub@set":      {"", goldenProjectHub, nil},
	"_scheduled_list":       {"", goldenSchedule, nil},
	"_scheduled_list@empty": {"", goldenSchedule, nil},
	"_schedule_form":        {"", goldenScheduleForm, nil},
	"_schedule_form@fixed":  {"", goldenScheduleForm, nil},
	"_schedule_form@new":    {"", goldenScheduleForm, nil},
	"_schedule_runs":        {"", goldenRunList, nil},
	"_schedule_runs@empty":  {"", goldenRunList, nil},
	"_schedule_runs@no-job": {"", goldenRunList, nil},
}

func TestGoldenAssistantPages(t *testing.T) {
	for name, spec := range assistantPages {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			f := goldenFrame(c, spec.frame)
			f.Data = spec.data(c)
			var out bytes.Buffer
			if err := goldenEngine(t, c).Page(&out, strings.SplitN(name, "@", 2)[0]+".gohtml", f); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String(), spec.rewrites...)
		})
	}
}

func TestGoldenAssistantFragments(t *testing.T) {
	for name, spec := range assistantFragments {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			f := goldenFrame(c, spec.frame)
			f.Data = spec.data(c)
			var out bytes.Buffer
			if err := goldenEngine(t, c).Fragment(&out, strings.SplitN(name, "@", 2)[0]+".gohtml", f); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String(), spec.rewrites...)
		})
	}
}
