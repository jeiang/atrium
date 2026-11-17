package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/db"
)

func linksSite(t *testing.T) (*apiSite, string, *db.DB, string) {
	t.Helper()
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	t.Cleanup(func() { _ = d.Close() })
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return &apiSite{t: t, base: base}, token, d, filepath.Join(dir, "media-contacts")
}

func linkRelated(t *testing.T, page map[string]any, kind, id string) map[string]any {
	t.Helper()
	for _, item := range page["related"].([]any) {
		m := item.(map[string]any)
		if m["target_kind"] == kind && m["target_id"] == id && m["link_id"] != nil {
			return m
		}
	}
	return nil
}

func TestKnowledgeTypedLinks(t *testing.T) {
	t.Parallel()
	s, token, d, contactsDir := linksSite(t)
	if n := asstCount(t, d, "SELECT COUNT(*) FROM schema_migrations WHERE version = 4"); n != 1 {
		t.Fatalf("migration 4 recorded %d times", n)
	}
	if err := os.MkdirAll(contactsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	vcf := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:card-rui-7\r\nFN:Rui Costa\r\nEND:VCARD\r\n"
	if err := os.WriteFile(filepath.Join(contactsDir, "rui.vcf"), []byte(vcf), 0o600); err != nil {
		t.Fatal(err)
	}
	create := func(title, body string) int64 {
		t.Helper()
		return number(t, apiData(t, s.do(http.MethodPost, "/api/v1/knowledge", token, map[string]any{"title": title, "body": body, "scope": "work"}), http.StatusCreated)["id"])
	}
	ines := create("Ines Duarte", "Product lead.")
	harbor := create("Harbor project", "Rebuild of the ferry booking site.")
	task := number(t, apiData(t, s.do(http.MethodPost, "/api/v1/tasks", token, map[string]any{"title": "Send the harbor brief"}), http.StatusCreated)["task_id"])
	inesPage := fmt.Sprintf("/api/v1/knowledge/%d/page", ines)
	harborPage := fmt.Sprintf("/api/v1/knowledge/%d/page", harbor)
	linksPath := fmt.Sprintf("/api/v1/knowledge/%d/links", ines)
	tag := expectConditional(t, s, token, harborPage)

	expectField(t, s.do(http.MethodPost, linksPath, token, map[string]any{"target_kind": "record", "target_id": fmt.Sprint(harbor), "relation": "mentions"}), "relation")
	expectField(t, s.do(http.MethodPost, linksPath, token, map[string]any{"target_kind": "thread", "target_id": "1", "relation": "about"}), "target_kind")
	expectAPI(t, s.do(http.MethodPost, linksPath, token, map[string]any{"target_kind": "record", "target_id": fmt.Sprint(ines), "relation": "see_also"}), http.StatusConflict, "refused")
	expectAPI(t, s.do(http.MethodPost, linksPath, token, map[string]any{"target_kind": "task", "target_id": "9999", "relation": "about"}), http.StatusConflict, "refused")
	expectAPI(t, s.do(http.MethodPost, linksPath, token, map[string]any{"target_kind": "contact", "target_id": "card-nobody", "relation": "about"}), http.StatusConflict, "refused")

	added := s.do(http.MethodPost, linksPath, token, map[string]any{"target_kind": "record", "target_id": fmt.Sprint(harbor), "relation": "part_of"})
	link := apiData(t, added, http.StatusCreated)
	effects, _ := added.json["effect_ids"].([]any)
	if target := link["target"].(map[string]any); target["title"] != "Harbor project" || target["state"] != "live" || target["address"] != fmt.Sprintf("/knowledge/%d", harbor) ||
		link["relation"] != "part_of" || link["writer"] != "operator" || link["run_id"] != nil || len(effects) != 1 {
		t.Fatalf("link: %s", added.body)
	}
	again := s.do(http.MethodPost, linksPath, token, map[string]any{"target_kind": "record", "target_id": fmt.Sprint(harbor), "relation": "part_of"})
	if apiData(t, again, http.StatusCreated)["id"] != link["id"] || again.json["effect_ids"] != nil {
		t.Fatalf("duplicate link: %s", again.body)
	}
	apiData(t, s.do(http.MethodPost, linksPath, token, map[string]any{"target_kind": "task", "target_id": fmt.Sprint(task), "relation": "about"}), http.StatusCreated)
	apiData(t, s.do(http.MethodPost, linksPath, token, map[string]any{"target_kind": "contact", "target_id": "card-rui-7", "relation": "works_with"}), http.StatusCreated)

	incoming := expectChanged(t, s, token, harborPage, tag)
	if back := linkRelated(t, incoming, "record", fmt.Sprint(ines)); back == nil || back["direction"] != "incoming" || back["relation"] != "part_of" || back["title"] != "Ines Duarte" {
		t.Fatalf("harbor page related: %v", incoming["related"])
	}
	page := apiData(t, s.do(http.MethodGet, inesPage, token, nil), http.StatusOK)
	if out := linkRelated(t, page, "record", fmt.Sprint(harbor)); out == nil || out["direction"] != "outgoing" || number(t, out["id"]) != harbor {
		t.Fatalf("ines page related: %v", page["related"])
	}
	if tk := linkRelated(t, page, "task", fmt.Sprint(task)); tk == nil || tk["title"] != "Send the harbor brief" || tk["state"] != "live" || tk["address"] != fmt.Sprintf("/tasks/%d", task) {
		t.Fatalf("task link: %v", page["related"])
	}
	if c := linkRelated(t, page, "contact", "card-rui-7"); c == nil || c["title"] != "Rui Costa" || c["state"] != "live" || c["address"] != "/contacts/card-rui-7" || number(t, c["id"]) != 0 {
		t.Fatalf("contact link: %v", page["related"])
	}
	if contacts := apiData(t, s.do(http.MethodGet, "/api/v1/contacts?q=Rui", token, nil), http.StatusOK); contacts["results"].([]any)[0].(map[string]any)["uid"] != "card-rui-7" {
		t.Fatalf("contact uid: %v", contacts)
	}

	m := apiData(t, s.do(http.MethodGet, fmt.Sprintf("/api/v1/knowledge/%d/map", ines), token, nil), http.StatusOK)
	typed := false
	for _, e := range m["edges"].([]any) {
		edge := e.(map[string]any)
		typed = typed || (number(t, edge["from"]) == ines && number(t, edge["to"]) == harbor && edge["kind"] == "part_of")
	}
	if !typed || len(m["nodes"].([]any)) != 2 {
		t.Fatalf("map: %v", m)
	}

	listTag := expectConditional(t, s, token, linksPath)
	if _, err := d.Write.Exec("UPDATE tasks SET title = 'Send the final harbor brief' WHERE id = ?", task); err != nil {
		t.Fatal(err)
	}
	list := expectChanged(t, s, token, linksPath, listTag)
	if out := list["outgoing"].([]any); len(out) != 3 || out[1].(map[string]any)["target"].(map[string]any)["title"] != "Send the final harbor brief" || len(list["incoming"].([]any)) != 0 {
		t.Fatalf("links: %v", list)
	}
	if _, err := d.Write.Exec("DELETE FROM tasks WHERE id = ?", task); err != nil {
		t.Fatal(err)
	}
	if tk := linkRelated(t, apiData(t, s.do(http.MethodGet, inesPage, token, nil), http.StatusOK), "task", fmt.Sprint(task)); tk == nil || tk["state"] != "missing" {
		t.Fatalf("deleted task link: %v", tk)
	}

	linkID := number(t, link["id"])
	removed := s.do(http.MethodPost, fmt.Sprintf("/api/v1/knowledge/links/%d/retract", linkID), token, nil)
	removedEffects, _ := removed.json["effect_ids"].([]any)
	if apiData(t, removed, http.StatusOK)["removed"] != true || len(removedEffects) != 1 {
		t.Fatalf("unlink: %s", removed.body)
	}
	if linkRelated(t, apiData(t, s.do(http.MethodGet, harborPage, token, nil), http.StatusOK), "record", fmt.Sprint(ines)) != nil {
		t.Fatal("removed link still shows")
	}
	if undo := s.do(http.MethodPost, fmt.Sprintf("/api/v1/undo/write:%d", number(t, effects[0])), token, nil); undo.status == http.StatusOK {
		t.Fatalf("undo of an already removed link succeeded: %s", undo.body)
	}
	if undo := s.do(http.MethodPost, fmt.Sprintf("/api/v1/undo/write:%d", number(t, removedEffects[0])), token, nil); undo.status != http.StatusOK {
		t.Fatalf("undo unlink: %d %s", undo.status, undo.body)
	}
	if linkRelated(t, apiData(t, s.do(http.MethodGet, harborPage, token, nil), http.StatusOK), "record", fmt.Sprint(ines)) == nil {
		t.Fatal("undo did not restore the link")
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/knowledge/links/99999/retract", token, nil), http.StatusNotFound, "not_found")

	if got := s.do(http.MethodDelete, fmt.Sprintf("/api/v1/knowledge/%d", harbor), token, nil); got.status != http.StatusNoContent {
		t.Fatalf("purge: %d %s", got.status, got.body)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM record_links WHERE target_kind = 'record' AND target_id = ?", fmt.Sprint(harbor)); n != 0 {
		t.Fatalf("purge left %d links", n)
	}
	if out := apiData(t, s.do(http.MethodGet, linksPath, token, nil), http.StatusOK)["outgoing"].([]any); len(out) != 2 {
		t.Fatalf("links after purge: %v", out)
	}
}
