package runner_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/runner"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/skills"
	"github.com/jeiang/atrium/internal/assistant/subagents/research"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbtest"
)

const now = 1_800_000_000.0

func TestProductionRunnerCarriesEveryHook(t *testing.T) {
	r, err := runner.New()
	if err != nil {
		t.Fatal(err)
	}
	if r.SkillsIndex == nil || r.ExpandSkills == nil || r.ResearchBatch == nil || r.Registry == nil {
		t.Fatalf("runner = %+v", r)
	}
	for _, kind := range []tool.Kind{tool.Chat, tool.Cron} {
		got, err := r.Registry.For(kind)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(got, func(tl tool.Tool) bool { return tl.Name == research.Name }) {
			t.Errorf("%s runs do not offer research", kind)
		}
	}
	d := dbtest.Schema(t)
	bound, err := r.Registry.Run(t.Context(), tool.Chat, nil, d.Read)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range bound {
		if tl.Name == research.Name && !strings.Contains(tl.Description, "Roles: calendar: ") {
			t.Errorf("research is not bound to the enabled agents: %q", tl.Description)
		}
	}
}

func TestRunsExpandSkillsAndRecordThem(t *testing.T) {
	d := dbtest.Schema(t)
	dir := t.TempDir()
	env := config.Map(map[string]string{
		"ATRIUM_DB": filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"), "ATRIUM_MODE": "shadow", "ATRIUM_TZ": "America/Port_of_Spain",
	})
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for name, body := range map[string]string{"review": "Review steps /skill:inbox", "inbox": "Inbox steps"} {
		if err := skills.Save(ctx, d.Write, name, "About "+name, body, now); err != nil {
			t.Fatal(err)
		}
	}
	r, err := runner.New()
	if err != nil {
		t.Fatal(err)
	}
	srv := anthropictest.New(t)
	svcCtx, cancel := context.WithCancel(context.Background())
	svc := service.New(svcCtx, d, cfg, env, r)
	svc.Client = srv.Client()
	t.Cleanup(func() {
		svc.Close()
		cancel()
	})
	tick := now
	svc.Now = func() float64 { tick += 0.001; return tick }

	run := func(kind string) (int64, int64) {
		srv.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
		thread, err := service.CreateThread(ctx, d.Write, svc.Now(), threads.NewThread{Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		id, err := svc.RunOnce(ctx, thread, "Do /skill:review and /skill:missing", service.Start{Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		return thread, id
	}
	for i, kind := range []string{threads.Chat, threads.Cron} {
		thread, id := run(kind)
		sent := string(srv.Requests()[i].Body)
		for _, want := range []string{`<skill name=\"review\">`, `<skill name=\"inbox\">`, "Unknown skills", "missing"} {
			if !strings.Contains(sent, want) {
				t.Errorf("%s request lacks %q", kind, want)
			}
		}
		got, err := journal.GetRun(ctx, d.Read, id)
		if err != nil || got == nil {
			t.Fatal(err)
		}
		if used, _ := journal.SkillsOf(*got); !slices.Equal(used, []string{"review", "inbox"}) {
			t.Errorf("%s run skills = %v", kind, used)
		}
		loaded, err := skills.LoadedInThread(ctx, d.Read, thread)
		if err != nil || len(loaded) != 2 || !loaded["review"] || !loaded["inbox"] {
			t.Errorf("%s loaded = %v, %v", kind, loaded, err)
		}
	}
	_, id := run(threads.Sub)
	if sent := string(srv.Requests()[2].Body); strings.Contains(sent, "<skill") {
		t.Error("a subagent task was expanded: it is model-written")
	}
	got, _ := journal.GetRun(ctx, d.Read, id)
	if used, _ := journal.SkillsOf(*got); len(used) != 0 {
		t.Errorf("sub run skills = %v", used)
	}
}
