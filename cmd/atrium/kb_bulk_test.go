package main

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
)

func TestKnowledgeConfirmSelectedPageReportsCommittedPrefix(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := asstBrowse(t, base)
	ids := make([]string, 0, 3)
	for _, title := range []string{"First", "Refused", "Later"} {
		id := knowledgeCreate(t, w, url.Values{"title": {title}, "body": {"Keep this fact"}, "scope": {"personal"}})
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	if code, page, _ := w.post(fmt.Sprintf("/library/knowledge/%s/retract", ids[1]), url.Values{}, false); code != http.StatusSeeOther {
		t.Fatalf("retract second record = %d: %.300s", code, page)
	}
	code, page, _ := w.post("/library/knowledge/confirm", url.Values{"scope": {"personal"}, "state": {"unconfirmed"}, "id": ids}, false)
	if code != http.StatusConflict || !strings.Contains(page, "1 confirmed") || strings.Contains(page, "Nothing confirmed") {
		t.Fatalf("committed-prefix refusal page = %d: %s", code, page)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	for i, id := range ids {
		var confirmed bool
		if err := d.Read.QueryRow("SELECT confirmed_at IS NOT NULL FROM kb_records WHERE id = ?", id).Scan(&confirmed); err != nil || confirmed != (i == 0) {
			t.Fatalf("record %s confirmed = %v (%v), want %v", id, confirmed, err, i == 0)
		}
	}
}
