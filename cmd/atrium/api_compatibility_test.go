package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
)

func TestAPILegacyCompatibility(t *testing.T) {
	dir, model, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, model)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	token := localAPIToken(t, "compatibility")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	s := &apiSite{t: t, base: base}
	model.Push(anthropictest.Reply("end_turn", anthropictest.Text("hello back")))
	made := s.do(http.MethodPost, "/api/v1/threads", token, map[string]any{"text": "hello"})
	if made.status != http.StatusCreated {
		t.Fatalf("create: %d %s", made.status, made.body)
	}
	id := number(t, made.json["thread"].(map[string]any)["id"])
	s.events(token, id, number(t, made.json["run_id"]), "")
	second := s.do(http.MethodPost, "/api/v1/threads", token, map[string]any{})
	if second.status != http.StatusCreated {
		t.Fatalf("second create: %d %s", second.status, second.body)
	}
	encode := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	list := s.do(http.MethodGet, "/api/v1/threads?limit=1", token, nil)
	items := apiReplyItems(t, list)
	row := items[0].(map[string]any)
	legacy := encode(map[string]any{"Pinned": row["pinned"], "UpdatedAt": row["updated_at"], "ID": row["id"]})
	if next := s.do(http.MethodGet, "/api/v1/threads?limit=1&cursor="+legacy, token, nil); len(apiReplyItems(t, next)) != 1 {
		t.Fatalf("legacy list: %s", next.body)
	}
	path := fmt.Sprintf("/api/v1/threads/%d", id)
	page := s.do(http.MethodGet, path+"?limit=1", token, nil)
	pageItems := page.json["items"].([]any)
	seq := pageItems[0].(map[string]any)["seq"]
	older := s.do(http.MethodGet, path+"?limit=1&cursor="+encode(map[string]any{"seq": seq}), token, nil)
	if older.status != http.StatusOK || len(older.json["items"].([]any)) != 1 {
		t.Fatalf("legacy transcript: %d %s", older.status, older.body)
	}
	for _, endpoint := range []string{"/api/v1/threads?limit=1", path + "?limit=1"} {
		first := s.do(http.MethodGet, endpoint, token, nil)
		cursor := first.json["next_cursor"].(string)
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			t.Fatal(err)
		}
		var envelope map[string]any
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		envelope["last"] = "wrong type"
		expectAPI(t, s.do(http.MethodGet, endpoint+"&cursor="+encode(envelope), token, nil), http.StatusBadRequest, "bad_cursor")
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/threads?project_id="+url.QueryEscape(" 1 "), token, nil), http.StatusOK, "")
	bad := s.do(http.MethodGet, "/api/v1/threads?project_id=wat", token, nil)
	expectAPI(t, bad, http.StatusUnprocessableEntity, "invalid")
	fields := bad.json["error"].(map[string]any)["fields"].([]any)
	if fields[0].(map[string]any)["type"] != "int_parsing" {
		t.Fatalf("integer field: %s", bad.body)
	}
	view := s.do(http.MethodGet, "/api/v1/threads?view=wrong", token, nil)
	expectAPI(t, view, http.StatusUnprocessableEntity, "invalid")
	if view.json["error"].(map[string]any)["fields"] != nil {
		t.Fatalf("view fields: %s", view.body)
	}
	expectAPI(t, s.do(http.MethodPost, path+"/stop", token, "not json", "Content-Type", "text/plain"), http.StatusConflict, "no_active_run")
	empty := s.do(http.MethodPost, "/api/v1/threads", token, nil)
	expectAPI(t, empty, http.StatusBadRequest, "bad_request")
	if !strings.Contains(empty.body, "The request body is empty.") {
		t.Fatalf("empty body: %s", empty.body)
	}
	for _, cap := range []any{-1, "wrong"} {
		expectAPI(t, s.do(http.MethodPost, "/api/v1/threads", token, map[string]any{"text": "hello", "cost_cap_usd": cap}), http.StatusUnprocessableEntity, "invalid")
	}
	start := len(model.Requests())
	capped := s.do(http.MethodPost, "/api/v1/threads", token, map[string]any{"text": "capped", "cost_cap_usd": 0})
	if capped.status != http.StatusCreated || capped.json["run_id"] == nil {
		t.Fatalf("capped create: %d %s", capped.status, capped.body)
	}
	cappedID := number(t, capped.json["thread"].(map[string]any)["id"])
	waitRunState(t, d, cappedID, "failed")
	if len(model.Requests()) != start {
		t.Fatal("cost cap dispatched a provider request")
	}
	asstExec(t, d, "UPDATE connections SET enabled = 0, state = 'unconfigured', reason = '' WHERE id = 'service/anthropic'")
	threads := asstCount(t, d, "SELECT COUNT(*) FROM threads")
	missing := s.do(http.MethodPost, "/api/v1/threads", token, map[string]any{"text": "missing connection"})
	if missing.status < 400 {
		t.Fatalf("missing connection: %d %s", missing.status, missing.body)
	}
	if asstCount(t, d, "SELECT COUNT(*) FROM threads") != threads || len(model.Requests()) != start {
		t.Fatal("missing connection created a thread or dispatched a provider request")
	}
}

func TestAPIDisabledAssistantBeforeBody(t *testing.T) {
	dir, _, f := moduleFixture(t, flagsExcept("assistant", "false"))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	token := localAPIToken(t, "disabled-compatibility")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	s := &apiSite{t: t, base: base}
	for _, body := range []string{"{", strings.Repeat("x", 2<<20)} {
		expectAPI(t, s.do(http.MethodPost, "/api/v1/threads", token, body), http.StatusConflict, "module_disabled")
	}
	expectAPI(t, s.do(http.MethodPut, "/api/v1/assistant/defaults", token, map[string]any{}, "Idempotency-Key", "defaults-1"), http.StatusConflict, "module_disabled")
}
