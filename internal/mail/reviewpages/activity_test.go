package reviewpages

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/toolset"
	"github.com/jeiang/atrium/internal/py"
)

func (s *site) thread() {
	s.exec("INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (1, 'chat', 'low', 0, 0)")
}

func (s *site) write(ts float64, tool, target string, thread any, before, after, undo any) int64 {
	s.t.Helper()
	res, err := s.db.Write.Exec("INSERT INTO agent_writes (ts, thread_id, tool, target, before_json, after_json, undo_json) VALUES (?, ?, ?, ?, ?, ?, ?)",
		ts, thread, tool, target, before, after, undo)
	if err != nil {
		s.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (s *site) action(ts float64, kind string, src, dest, undo any, ok any) int64 {
	s.t.Helper()
	res, err := s.db.Write.Exec("INSERT INTO auto_actions (ts, account, message_id, kind, src, dest, undo, ok) VALUES (?, 'icloud', 7, ?, ?, ?, ?, ?)",
		ts, kind, src, dest, undo, ok)
	if err != nil {
		s.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestActivityMergesMailAndWritesAndFiltersByCategory(t *testing.T) {
	s := newSite(t)
	s.thread()
	profile := s.write(100, "profile_set", "profile:tone", 1, `{"k":"tone"}`, `{"k":"tone","v":"brief"}`, `{"op":"fake"}`)
	s.write(105, "notion_update_page", "page", nil, nil, nil, nil)
	s.write(106, "payee_rename", "payee:1", nil, nil, nil, `{"op": "money_restore"}`)
	s.action(110, "flag", "INBOX", nil, `{"op":"unflag"}`, 1)
	page := status(t, s.get("/activity"), http.StatusOK)
	order := []string{`id="activity-mail-`, "payee_rename", "notion_update_page", "profile_set"}
	last := -1
	for _, want := range order {
		at := strings.Index(page, want)
		if at <= last {
			t.Fatalf("%q is out of order (at %d, after %d)", want, at, last)
		}
		last = at
	}
	mustContain(t, page, fmt.Sprintf("/activity/write/%d/undo", profile), "/activity/mail/1/undo", `href="/chat/1"`, "chat thread", "Before and after")
	for _, c := range []struct{ category, present, absent string }{
		{"mail", "activity-mail-", "profile_set"},
		{"assistant", "profile_set", "notion_update_page"},
		{"wiki", "notion_update_page", "profile_set"},
		{"money", "payee_rename", "notion_update_page"},
	} {
		filtered := status(t, s.get("/activity?category="+c.category), http.StatusOK)
		mustContain(t, filtered, c.present)
		mustNotContain(t, filtered, c.absent)
	}
	status(t, s.get("/activity?category=bogus"), http.StatusUnprocessableEntity)
	mustContain(t, status(t, s.get("/activity?category=media"), http.StatusOK), "Nothing in media has been changed yet.")
}

func TestActivityPagesThroughShowOlder(t *testing.T) {
	s := newSite(t)
	for i := range 60 {
		s.write(float64(1000+i), "profile_set", fmt.Sprintf("profile:k%02d", i), nil, nil, nil, nil)
	}
	first := status(t, s.get("/activity"), http.StatusOK)
	mustContain(t, first, `id="load-more"`, `hx-get="/activity?category=&amp;offset=50"`, "profile:k59", "profile:k10")
	mustNotContain(t, first, "profile:k09")
	more := status(t, s.get("/activity?category=&offset=50", hx, "true"), http.StatusOK)
	mustContain(t, more, "profile:k09", "profile:k00")
	mustNotContain(t, more, `id="load-more"`, "<html", "profile:k10")
	full := status(t, s.get("/activity?offset=50"), http.StatusOK)
	mustContain(t, full, "<html", "profile:k09")
	again := status(t, s.get("/activity?offset=0", hx, "true"), http.StatusOK)
	mustContain(t, again, "<html")
	clamped := status(t, s.get("/activity?offset=-5", hx, "true"), http.StatusOK)
	mustContain(t, clamped, "<html", "profile:k59")
	rec := s.get("/activity?offset=x")
	body := status(t, rec, http.StatusUnprocessableEntity)
	if rec.Header().Get("Content-Type") != "application/json" || jsonOf(t, body)["detail"] == nil {
		t.Errorf("offset=x answered %s %s", rec.Header().Get("Content-Type"), body)
	}
}

func TestActivityMailRowsWithoutPlacesShowGone(t *testing.T) {
	s := newSite(t)
	s.action(100, "file", nil, nil, nil, 0)
	page := status(t, s.get("/activity?category=mail"), http.StatusOK)
	mustContain(t, page, "Gone", "(no subject)", "Skipped")
	mustNotContain(t, page, "Undo this")
}

func TestUndoingAWriteWithoutARegisteredUndoerIsRefused(t *testing.T) {
	s := newSite(t)
	id := s.write(100, "profile_set", "profile:tone", nil, nil, nil, `{"op":"ghost"}`)
	body := status(t, s.post(fmt.Sprintf("/activity/write/%d/undo", id), nil), http.StatusConflict)
	if jsonOf(t, body)["detail"] != "unknown undo kind 'ghost'" {
		t.Errorf("body = %s", body)
	}
}

func (s *site) profileSet(run int64, key, value string) map[string]any {
	s.t.Helper()
	reg, err := toolset.Registry()
	if err != nil {
		s.t.Fatal(err)
	}
	tools, err := reg.All()
	if err != nil {
		s.t.Fatal(err)
	}
	i := slices.IndexFunc(tools, func(tl tool.Tool) bool { return tl.Name == "profile_set" })
	tc := &tool.Context{DB: s.db, Cfg: s.cfg, Env: s.env, Kind: tool.Chat, ThreadID: 1, RunID: run, Now: func() float64 { return 100 }}
	out, err := tools[i].Handler(context.Background(), tc, py.Dict{{Key: "key", Value: key}, {Key: "value", Value: value}})
	if err != nil {
		s.t.Fatal(err)
	}
	return jsonOf(s.t, py.JSONDumps(out))
}

func (s *site) profile(key string) any {
	s.t.Helper()
	var value string
	err := s.db.Read.QueryRow("SELECT value FROM agent_profile WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		s.t.Fatal(err)
	}
	return value
}

func TestUndoingAProfileSetRestoresThePreviousValue(t *testing.T) {
	s := newSite(t)
	s.thread()
	run, err := journal.StartRun(context.Background(), s.db.Write, 1, "chat", "m", "medium", 100, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := s.profileSet(run, "tz", "AST")
	second := s.profileSet(run, "tz", "UTC")
	if first["previous"] != nil || second["previous"] != "AST" || s.profile("tz") != "UTC" {
		t.Fatalf("profile_set %v %v", first, second)
	}
	page := status(t, s.get("/activity?category=assistant"), http.StatusOK)
	mustContain(t, page, "profile:tz", fmt.Sprintf("/activity/write/%v/undo", second["write_id"]))
	body := status(t, s.post(fmt.Sprintf("/activity/write/%v/undo", second["write_id"]), nil, hx, "true"), http.StatusOK)
	mustContain(t, body, "Undone", fmt.Sprintf("Change %v was reverted.", second["write_id"]))
	if s.profile("tz") != "AST" {
		t.Errorf("profile tz = %v after undo", s.profile("tz"))
	}
	rec := s.post(fmt.Sprintf("/activity/write/%v/undo", first["write_id"]), nil)
	status(t, rec, http.StatusSeeOther)
	if s.profile("tz") != nil {
		t.Errorf("profile tz = %v after undoing the first write", s.profile("tz"))
	}
	third := s.profileSet(run, "tz", "AST")
	s.profileSet(run, "tz", "UTC")
	refused := s.post(fmt.Sprintf("/activity/write/%v/undo", third["write_id"]), nil)
	if got := status(t, refused, http.StatusConflict); jsonOf(t, got)["detail"] != "profile 'tz' changed since this write" {
		t.Errorf("detail = %s", got)
	}
	if s.profile("tz") != "UTC" {
		t.Errorf("a refused undo changed the profile: %v", s.profile("tz"))
	}
}
