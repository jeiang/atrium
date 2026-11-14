package main

import (
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/secrets"
)

func TestSettingsAPI(t *testing.T) {
	f := newFixture(t, goldenData{}, nil, "")
	pinned := module.FlagKey(module.Mail)
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, f.dir, "host.toml", "[core]\n"+pinned+" = false\n"))
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}
	d := mustConnect(t, filepath.Join(f.dir, "atrium.db"), keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	audits := func(key string) (n int) {
		t.Helper()
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM settings_audit WHERE key = ?", key).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	row := func(items []any, key string) map[string]any {
		t.Helper()
		for _, it := range items {
			if r := it.(map[string]any); r["key"] == key {
				return r
			}
		}
		t.Fatalf("no %s in %v", key, items)
		return nil
	}

	group := "/api/v1/settings?group=assistant"
	tag := expectConditional(t, s, token, group)
	if r := row(apiItems(t, apiData(t, s.do(http.MethodGet, group, token, nil), http.StatusOK)), "monthly_budget_usd"); r["value"] != "150" || r["source"] != "default" {
		t.Fatalf("default budget: %v", r)
	}
	if got := apiData(t, s.do(http.MethodPut, "/api/v1/settings/monthly_budget_usd", token, map[string]any{"value": "200"}), http.StatusOK); got["keys"].([]any)[0] != "monthly_budget_usd" {
		t.Fatalf("set: %v", got)
	}
	if r := row(apiItems(t, expectChanged(t, s, token, group, tag)), "monthly_budget_usd"); r["value"] != "200" || r["source"] != "db" {
		t.Fatalf("budget after set: %v", r)
	}
	var source string
	if err := d.Read.QueryRow("SELECT source FROM settings_audit WHERE key = 'monthly_budget_usd' ORDER BY rowid DESC LIMIT 1").Scan(&source); err != nil || source != "api" {
		t.Fatalf("settings audit source = %q, %v", source, err)
	}

	before := audits("monthly_budget_usd")
	expectField(t, s.do(http.MethodPut, "/api/v1/settings/monthly_budget_usd", token, map[string]any{"value": "0"}), "value")
	if after := audits("monthly_budget_usd"); after != before {
		t.Fatalf("a refused value wrote %d settings audit rows", after-before)
	}
	expectAPI(t, s.do(http.MethodPut, "/api/v1/settings/"+pinned, token, map[string]any{"value": "true"}), http.StatusConflict, "refused")
	expectAPI(t, s.do(http.MethodPut, "/api/v1/settings/"+secrets.ActionLinkKey, token, map[string]any{"value": "x"}), http.StatusConflict, "refused")
	for _, key := range []string{"mail_mode"} {
		before := audits(key)
		expectAPI(t, s.do(http.MethodPut, "/api/v1/settings/"+key, token, map[string]any{"value": "live"}), http.StatusConflict, "refused")
		expectAPI(t, s.do(http.MethodDelete, "/api/v1/settings/"+key, token, nil), http.StatusConflict, "refused")
		var stored int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM settings WHERE key = ?", key).Scan(&stored); err != nil || stored != 0 || audits(key) != before {
			t.Fatalf("%s changed through the API: stored %d, audits %d -> %d, %v", key, stored, before, audits(key), err)
		}
	}
	if items := apiItems(t, apiData(t, s.do(http.MethodGet, "/api/v1/settings?group=mail&limit=100", token, nil), http.StatusOK)); slices.ContainsFunc(items, func(it any) bool { return it.(map[string]any)["key"] == "mail_mode" }) {
		t.Fatalf("mail_mode is listed although no settings page shows it: %v", items)
	}
	exports := "/api/v1/settings?group=export&limit=100"
	expectAPI(t, s.do(http.MethodPut, "/api/v1/settings/export_notion_enabled", token, map[string]any{"value": "true"}), http.StatusUnprocessableEntity, "invalid")
	if r := row(apiItems(t, apiData(t, s.do(http.MethodGet, exports, token, nil), http.StatusOK)), "export_notion_enabled"); r["value"] != "false" {
		t.Fatalf("a refused export switch was saved: %v", r)
	}
	page := "0123456789abcdef0123456789abcdef"
	apiData(t, s.do(http.MethodPut, "/api/v1/settings/export_notion_parent", token, map[string]any{"value": page}), http.StatusOK)
	apiData(t, s.do(http.MethodPut, "/api/v1/settings/export_notion_private_to", token, map[string]any{"value": page}), http.StatusOK)
	if r := row(apiItems(t, apiData(t, s.do(http.MethodGet, exports, token, nil), http.StatusOK)), "export_notion_private_to"); r["value"] == "" {
		t.Fatalf("private_to matching the parent was not kept: %v", r)
	}
	apiData(t, s.do(http.MethodPut, "/api/v1/settings/export_notion_parent", token, map[string]any{"value": "fedcba9876543210fedcba9876543210"}), http.StatusOK)
	if r := row(apiItems(t, apiData(t, s.do(http.MethodGet, exports, token, nil), http.StatusOK)), "export_notion_private_to"); r["value"] != "" {
		t.Fatalf("private_to survived a parent change: %v", r)
	}
	apiData(t, s.do(http.MethodDelete, "/api/v1/settings/monthly_budget_usd", token, nil), http.StatusOK)
	if r := row(apiItems(t, apiData(t, s.do(http.MethodGet, group, token, nil), http.StatusOK)), "monthly_budget_usd"); r["value"] != "150" {
		t.Fatalf("budget after unset: %v", r)
	}

	all := apiData(t, s.do(http.MethodGet, "/api/v1/settings?limit=20", token, nil), http.StatusOK)
	next, _ := all["next_cursor"].(string)
	if len(apiItems(t, all)) != 20 || next == "" {
		t.Fatalf("first settings page: %d items, cursor %q", len(apiItems(t, all)), next)
	}
	second := apiData(t, s.do(http.MethodGet, "/api/v1/settings?limit=20&cursor="+next, token, nil), http.StatusOK)
	if items := apiItems(t, second); len(items) == 0 || items[0].(map[string]any)["key"].(string) <= apiItems(t, all)[19].(map[string]any)["key"].(string) {
		t.Fatalf("second settings page: %v", items)
	}
	expectAPI(t, s.do(http.MethodGet, group+"&cursor="+next, token, nil), http.StatusBadRequest, "bad_cursor")

	const value = "api-secret-value-never-returned"
	var replies []apiReply
	set := s.do(http.MethodPut, "/api/v1/settings/secrets/"+secrets.ActionLinkKey, token, map[string]any{"value": value})
	if got := apiData(t, set, http.StatusOK); got["status"] != "set" {
		t.Fatalf("secret set: %v", got)
	}
	status := s.do(http.MethodGet, "/api/v1/settings/secrets", token, nil)
	replies = append(replies, set, status)
	found := false
	for _, it := range apiData(t, status, http.StatusOK)["secrets"].([]any) {
		if r := it.(map[string]any); r["name"] == secrets.ActionLinkKey {
			found = r["status"] == "set"
		}
	}
	if !found {
		t.Fatalf("secret status: %s", status.body)
	}
	refused := s.do(http.MethodPut, "/api/v1/settings/secrets/key", token, map[string]any{"value": strings.Repeat("ab", 32)})
	expectAPI(t, refused, http.StatusConflict, "refused")
	deleted := s.do(http.MethodDelete, "/api/v1/settings/secrets/"+secrets.ActionLinkKey, token, nil)
	if got := apiData(t, deleted, http.StatusOK); got["status"] == "set" {
		t.Fatalf("secret delete: %v", got)
	}
	replies = append(replies, refused, deleted)
	for _, r := range replies {
		if strings.Contains(r.body, value) {
			t.Fatalf("a response carried the secret value: %s", r.body)
		}
	}
	var leaked int
	if err := d.Read.QueryRow("SELECT (SELECT COUNT(*) FROM operation_audit WHERE details_json LIKE ?) + (SELECT COUNT(*) FROM settings_audit WHERE COALESCE(old_value, '') || COALESCE(new_value, '') LIKE ?)", "%"+value+"%", "%"+value+"%").Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("the secret value reached an audit row: %d %v", leaked, err)
	}
}
