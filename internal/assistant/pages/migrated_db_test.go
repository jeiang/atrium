package pages

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

func TestThreadModelMigratesExistingDatabase(t *testing.T) {
	d, path := dbtest.Copy(t, filepath.Join(golden.Dir(), "atrium.db"))
	if _, err := d.Write.Exec("ALTER TABLE threads DROP COLUMN model"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("DELETE FROM schema_migrations WHERE version = 7"); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM pragma_table_info('threads') WHERE name = 'model'").Scan(&left); err != nil || left != 0 {
		t.Fatalf("model column present before migration: %d %v", left, err)
	}
	if err := db.Migrate(t.Context(), d, path); err != nil {
		t.Fatal(err)
	}

	s := newSiteOn(t, d)
	s.api.Push(reply1(anthropictest.Text("ok")))
	r := s.multipart("/chat", map[string]string{"text": "hi", "model": "claude-sonnet-5-5"}, nil, hx, "true")
	status(t, r, http.StatusNoContent)
	thread, err := strconv.ParseInt(strings.TrimPrefix(r.Header.Get("HX-Redirect"), "/chat/"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	s.settle(s.lastRun(thread))
	mustContain(t, status(t, s.get(fmt.Sprintf("/chat/%d", thread)), http.StatusOK), `<option value="claude-sonnet-5-5" selected>`)
	var model string
	if err := d.Read.QueryRow("SELECT model FROM threads WHERE id = ?", thread).Scan(&model); err != nil || model != "claude-sonnet-5-5" {
		t.Fatalf("thread model %q %v", model, err)
	}
}
