package main

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
)

func TestKnowledgeSelectedRecordsConfirmTogetherAndHomeShowsReceipts(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := asstBrowse(t, base)

	var ids []int64
	for _, title := range []string{"Boiler code", "Boiler service", "Boiler brand"} {
		ids = append(ids, knowledgeCreate(t, w, url.Values{"title": {title}, "body": {title + " details."}, "scope": {"work"}, "untrusted": {"1"}}))
	}
	code, body, _ := w.get("/library/knowledge", false)
	if code != http.StatusOK || !strings.Contains(body, "Confirm selected") || !strings.Contains(body, fmt.Sprintf(`name="id" value="%d"`, ids[0])) {
		t.Fatalf("knowledge list = %d, select controls missing: %.300s", code, body)
	}
	if code, body, _ := w.post("/library/knowledge/confirm", url.Values{"id": {fmt.Sprint(ids[0]), fmt.Sprint(ids[1])}}, false); code != http.StatusSeeOther {
		t.Fatalf("confirm selected = %d: %s", code, body)
	}
	for i, want := range []bool{true, true, false} {
		if _, confirmed, _ := knowledgeMarks(t, d, ids[i]); confirmed != want {
			t.Errorf("record %d confirmed = %v, want %v", ids[i], confirmed, want)
		}
	}
	if code, _, _ := w.post("/library/knowledge/confirm", url.Values{}, false); code == http.StatusSeeOther {
		t.Errorf("confirming nothing = %d, want a refusal", code)
	}
	if _, confirmed, _ := knowledgeMarks(t, d, ids[2]); confirmed {
		t.Error("an empty selection confirmed a record")
	}

	if _, err := d.Write.Exec("INSERT INTO agent_writes (ts, tool, target, state, before_json, after_json, undo_json) VALUES (?, 'task_add', 'task:1', 'ok', '{}', '{}', '{}')", float64(time.Now().Unix())); err != nil {
		t.Fatal(err)
	}
	code, body, _ = w.get("/activity/recent", true)
	if code != http.StatusOK || !strings.Contains(body, "Added a task") || !strings.Contains(body, `hx-post="/activity/write/`) {
		t.Errorf("recent receipts = %d: %.400s", code, body)
	}
	code, body, _ = w.get("/chat", false)
	if code != http.StatusOK || !strings.Contains(body, "data-brief") || !strings.Contains(body, `id="brief-next"`) || !strings.Contains(body, `hx-get="/activity/recent"`) {
		t.Errorf("home = %d, brief or receipts placeholder missing", code)
	}
}
