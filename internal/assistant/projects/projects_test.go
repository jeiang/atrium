package projects_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
)

func ptr[T any](v T) *T { return &v }

var kitchen = projects.Fields{Name: "Kitchen", Instructions: "Always answer in metric units.", Effort: "high", ContextLimit: "200000"}

func create(t *testing.T, d *db.DB, now float64, f projects.Fields) int64 {
	t.Helper()
	id, err := projects.Create(t.Context(), d.Write, now, f)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustGet(t *testing.T, d *db.DB, id int64) projects.Project {
	t.Helper()
	p, err := projects.Get(t.Context(), d.Read, id)
	if err != nil || p == nil {
		t.Fatalf("Get(%d) = %v, %v", id, p, err)
	}
	return *p
}

func wantProjectError(t *testing.T, err error, msg string) {
	t.Helper()
	var pe projects.ProjectError
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), msg) {
		t.Fatalf("error %v, want a project error containing %q", err, msg)
	}
}

func TestCleanRefusals(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	for _, c := range []struct {
		f    projects.Fields
		want string
	}{
		{projects.Fields{Name: " ", Effort: "low", ContextLimit: 90000}, "Give the project a name."},
		{projects.Fields{Name: strings.Repeat("é", 81), Effort: "low", ContextLimit: 90000}, "The name is limited to 80 characters."},
		{projects.Fields{Name: "n", Instructions: strings.Repeat("x", 20_001), Effort: "low", ContextLimit: 90000}, "Instructions are limited to 20000 characters."},
		{projects.Fields{Name: "n", Effort: "ultra", ContextLimit: 90000}, "effort must be one of"},
		{projects.Fields{Name: "n", Effort: "low", ContextLimit: 5}, "context limit must be one of"},
		{projects.Fields{Name: "n", Effort: "low", ContextLimit: 90000, WikiHubID: "nonsense"}, "The wiki hub must be a Notion page id or URL."},
	} {
		if _, err := projects.Create(ctx, d.Write, 1, c.f); err == nil {
			t.Errorf("%+v was created", c.f)
		} else {
			wantProjectError(t, err, c.want)
		}
	}
	if _, err := projects.Create(ctx, d.Write, 1, projects.Fields{Name: strings.Repeat("é", 80), Effort: "low", ContextLimit: 90000}); err != nil {
		t.Errorf("an 80 character name: %v", err)
	}
}

func TestCreateUpdateGet(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	hub := "00000000-0000-0000-0000-0000000000aa"
	id := create(t, d, 10, projects.Fields{Name: "  Kitchen ", Instructions: " Use cm. \n", Effort: "xhigh", ContextLimit: "500000",
		WikiHubID: "https://www.notion.so/Kitchen-" + strings.ReplaceAll(strings.ToUpper(hub), "-", "")})
	p := mustGet(t, d, id)
	if p.Name != "Kitchen" || p.Instructions != "Use cm." || p.Effort != "xhigh" || p.ContextLimit != 500_000 || p.WikiHubID == nil || *p.WikiHubID != hub ||
		p.CreatedAt != 10 || p.UpdatedAt != 10 || p.ThreadCount != 0 || p.LastActivity != nil || p.Archived {
		t.Errorf("project %+v", p)
	}
	if ok, err := projects.Update(ctx, d.Write, id, 20, projects.Fields{Name: "Kitchen reno", Instructions: "", Effort: "low", ContextLimit: 90_000}); err != nil || !ok {
		t.Fatalf("Update = %v, %v", ok, err)
	}
	p = mustGet(t, d, id)
	if p.Name != "Kitchen reno" || p.Instructions != "" || p.WikiHubID != nil || p.UpdatedAt != 20 || p.CreatedAt != 10 {
		t.Errorf("updated project %+v", p)
	}
	if ok, err := projects.Update(ctx, d.Write, 999, 1, kitchen); err != nil || ok {
		t.Errorf("Update(unknown) = %v, %v", ok, err)
	}
	if _, err := projects.Update(ctx, d.Write, id, 1, projects.Fields{}); err == nil {
		t.Error("Update accepted empty fields")
	}
	if got, err := projects.Get(ctx, d.Read, 999); err != nil || got != nil {
		t.Errorf("Get(unknown) = %v, %v", got, err)
	}
	if ok, err := projects.SetHub(ctx, d.Write, id, hub, 30); err != nil || !ok {
		t.Fatal(err)
	}
	if p := mustGet(t, d, id); *p.WikiHubID != hub || p.UpdatedAt != 30 {
		t.Errorf("hub %+v", p)
	}
}

func TestListOrderAndArchive(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	create(t, d, 100, projects.Fields{Name: "Idle", Effort: "low", ContextLimit: 90000})
	busy := create(t, d, 50, projects.Fields{Name: "Busy", Effort: "low", ContextLimit: 90000})
	tied := create(t, d, 50, projects.Fields{Name: "Tied", Effort: "low", ContextLimit: 90000})
	_, err := threads.Create(ctx, d.Write, 200, threads.NewThread{Kind: threads.Chat, ProjectID: &busy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threads.Create(ctx, d.Write, 300, threads.NewThread{Kind: threads.Cron, ProjectID: &busy}); err != nil {
		t.Fatal(err)
	}
	archived, err := threads.Create(ctx, d.Write, 250, threads.NewThread{Kind: threads.Chat, ProjectID: &busy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threads.SetFlag(ctx, d.Write, archived, "archived", true); err != nil {
		t.Fatal(err)
	}
	list, err := projects.List(ctx, d.Read, false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range list {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "Busy,Idle,Tied" {
		t.Errorf("order %v", names)
	}
	if list[0].ThreadCount != 1 || *list[0].LastActivity != 250 || list[1].LastActivity != nil {
		t.Errorf("counts: %+v", list)
	}
	if ok, err := projects.SetArchived(ctx, d.Write, tied, true); err != nil || !ok {
		t.Fatal(err)
	}
	if got, _ := projects.List(ctx, d.Read, false); len(got) != 2 {
		t.Errorf("active list %d", len(got))
	}
	if got, _ := projects.List(ctx, d.Read, true); len(got) != 1 || got[0].ID != tied || !bool(got[0].Archived) {
		t.Errorf("archived list %+v", got)
	}
	if ok, err := projects.SetArchived(ctx, d.Write, 999, true); err != nil || ok {
		t.Errorf("SetArchived(unknown) = %v, %v", ok, err)
	}
}

func TestMoveThread(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	project := create(t, d, 1, kitchen)
	chat, _ := threads.Create(ctx, d.Write, 1, threads.NewThread{Kind: threads.Chat})
	cron, _ := threads.Create(ctx, d.Write, 1, threads.NewThread{Kind: threads.Cron})
	if ok, err := projects.MoveThread(ctx, d.Write, chat, &project); err != nil || !ok {
		t.Fatalf("move in = %v, %v", ok, err)
	}
	if th, _ := threads.Get(ctx, d.Read, chat); th.ProjectID == nil || *th.ProjectID != project {
		t.Errorf("thread %+v", th)
	}
	if ok, err := projects.MoveThread(ctx, d.Write, chat, nil); err != nil || !ok {
		t.Fatalf("move out = %v, %v", ok, err)
	}
	if th, _ := threads.Get(ctx, d.Read, chat); th.ProjectID != nil {
		t.Errorf("thread %+v", th)
	}
	if _, err := projects.MoveThread(ctx, d.Write, chat, ptr(int64(999))); err == nil {
		t.Error("moved into an unknown project")
	} else {
		wantProjectError(t, err, "unknown project")
	}
	if ok, err := projects.MoveThread(ctx, d.Write, cron, nil); err != nil || ok {
		t.Errorf("moving a cron thread = %v, %v", ok, err)
	}
	if _, err := projects.SetArchived(ctx, d.Write, project, true); err != nil {
		t.Fatal(err)
	}
	if _, err := projects.MoveThread(ctx, d.Write, chat, &project); err == nil {
		t.Error("moved into an archived project")
	}
}

func TestSystemBlock(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	hub := "00000000-0000-0000-0000-0000000000aa"
	both := create(t, d, 1, projects.Fields{Name: "Kitchen", Instructions: "Always answer in metric units.", Effort: "low", ContextLimit: 90000, WikiHubID: hub})
	hubOnly := create(t, d, 1, projects.Fields{Name: "Hub", Effort: "low", ContextLimit: 90000, WikiHubID: hub})
	empty := create(t, d, 1, projects.Fields{Name: "Empty", Effort: "low", ContextLimit: 90000})
	block := func(project *int64) *string {
		t.Helper()
		s, err := projects.SystemBlock(ctx, d.Read, threads.Thread{ProjectID: project})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if block(nil) != nil || block(ptr(int64(0))) != nil || block(ptr(int64(999))) != nil || block(&empty) != nil {
		t.Error("a thread without a project or with nothing to say has no block")
	}
	want := "This thread belongs to the operator's project “Kitchen”. Follow the project's instructions:\n<project_instructions>\nAlways answer in metric units.\n</project_instructions>\n\n" +
		"The project's wiki hub page has id " + hub + ". Keep it updated with durable project knowledge (decisions, status, open questions, sources) as you learn it: " +
		"load the wiki-capture skill (`/skill:wiki-capture`) and follow it, updating that hub page or pages under it rather than creating duplicates."
	if got := block(&both); got == nil || *got != want {
		t.Errorf("block\n got %v\nwant %s", got, want)
	}
	if got := block(&hubOnly); got == nil || strings.Contains(*got, "project_instructions") || !strings.Contains(*got, hub) {
		t.Errorf("hub-only block %v", got)
	}
}

func TestHubMarkdown(t *testing.T) {
	if got, want := projects.HubMarkdown(projects.Project{Name: "Kitchen"}), "Durable knowledge for the project “Kitchen”: decisions, status, open questions and links."; got != want {
		t.Errorf("got %q", got)
	}
	got := projects.HubMarkdown(projects.Project{Name: "Kitchen", Instructions: "Use cm."})
	if want := "Durable knowledge for the project “Kitchen”: decisions, status, open questions and links.\n\n## Project brief\n\nUse cm."; got != want {
		t.Errorf("got %q", got)
	}
}
