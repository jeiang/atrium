package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMailRulesAndDecisionsLinkBothWays(t *testing.T) {
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	m := mailSafetyFixture(t, "shadow")
	for i := range 3 {
		m.add("INBOX", fmt.Sprintf("Bill %d", i), "billing@shop.test", fmt.Sprintf("bill-%d", i), "Your bill is ready.", `\Seen`)
	}
	m.add("INBOX", "Big sale", "deals@promo.test", "promo-1", "Everything must go.", `\Seen`)
	rulesFile := filepath.Join(m.dir, "rules.json")
	if err := os.WriteFile(rulesFile, []byte(`{"rules":[`+
		`{"id":"bills","account":"icloud","action":"file","dest":"Bills","when":[{"field":"from_addr","op":"eq","value":"billing@shop.test"}]},`+
		`{"id":"promo","account":"icloud","action":"delete","when":[{"field":"from_addr","op":"eq","value":"deals@promo.test"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m.run(t, "rules", "import", rulesFile)
	m.run(t, "sync")
	d := mustConnect(t, m.path, keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := d.Read.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := count("SELECT COUNT(*) FROM decisions WHERE stage = 'operator' AND key = 'bills'"); got != 3 {
		t.Fatalf("sync recorded %d decisions by bills, want 3", got)
	}
	beforeRules, beforeDecisions := count("SELECT COUNT(*) FROM rules"), count("SELECT COUNT(*) FROM decisions")

	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, m.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stop() })
	settleBootChecks(t, m.dir, keyFor(t))
	s := &apiSite{t: t, base: base}

	counts := map[string]any{}
	for _, item := range apiItems(t, apiData(t, s.do(http.MethodGet, "/api/v1/mail/rules", token, nil), http.StatusOK)) {
		rule := item.(map[string]any)
		counts[rule["id"].(string)] = rule["decision_count"]
	}
	if counts["bills"] != float64(3) || counts["promo"] != float64(1) {
		t.Fatalf("rule decision counts = %v, want bills 3 and promo 1", counts)
	}

	decisions := "/api/v1/mail/rules/bills/decisions"
	first := apiData(t, s.do(http.MethodGet, decisions+"?limit=2", token, nil), http.StatusOK)
	rule := first["rule"].(map[string]any)
	if rule["id"] != "bills" || rule["title"] != "File into Bills" || rule["state"] != "current" || first["total"] != float64(3) {
		t.Fatalf("first page names %v with total %v", rule, first["total"])
	}
	items := apiItems(t, first)
	next, _ := first["next_cursor"].(string)
	if len(items) != 2 || next == "" {
		t.Fatalf("first page has %d items and cursor %q", len(items), next)
	}
	newer := items[0].(map[string]any)["decision_id"].(float64)
	for _, item := range items {
		row := item.(map[string]any)
		ref, _ := row["rule"].(map[string]any)
		if row["stage"] != "operator" || row["key"] != "bills" || ref["id"] != "bills" || ref["state"] != "current" {
			t.Fatalf("decision row %v", row)
		}
	}
	if older := items[1].(map[string]any)["decision_id"].(float64); older >= newer {
		t.Fatalf("decisions not most recent first: %v then %v", newer, older)
	}
	second := apiData(t, s.do(http.MethodGet, decisions+"?limit=2&cursor="+next, token, nil), http.StatusOK)
	if len(apiItems(t, second)) != 1 || second["next_cursor"] != nil {
		t.Fatalf("second page: %v", second)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/mail/rules/promo/decisions?cursor="+next, token, nil), http.StatusBadRequest, "bad_cursor")
	for _, limit := range []string{"0", "101"} {
		expectAPI(t, s.do(http.MethodGet, decisions+"?limit="+limit, token, nil), http.StatusUnprocessableEntity, "invalid")
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/mail/rules/nobody/decisions", token, nil), http.StatusNotFound, "not_found")

	triageRules := func() map[string]map[string]any {
		t.Helper()
		out := map[string]map[string]any{}
		for _, item := range apiItems(t, apiData(t, s.do(http.MethodGet, "/api/v1/mail/triage?stage=operator", token, nil), http.StatusOK)) {
			row := item.(map[string]any)
			ref, ok := row["rule"].(map[string]any)
			if !ok {
				t.Fatalf("operator decision without its rule: %v", row)
			}
			out[row["subject"].(string)] = ref
		}
		return out
	}
	if got := triageRules(); len(got) != 4 || got["Bill 0"]["title"] != "File into Bills" || got["Big sale"]["title"] != "Delete" || got["Big sale"]["state"] != "current" {
		t.Fatalf("decisions name their rules as %v", got)
	}

	status, body, _ := fetch(t, http.DefaultClient, http.MethodGet, base+"/rules", nil)
	if status != http.StatusOK || !strings.Contains(body, `data-decision-count="3"`) || !strings.Contains(body, "3 decisions") || !strings.Contains(body, "1 decision<") {
		t.Fatalf("rules page = %d without decision counts", status)
	}
	status, body, _ = fetch(t, http.DefaultClient, http.MethodGet, base+"/triage?stage=operator", nil)
	if status != http.StatusOK || !strings.Contains(body, `data-rule-ref="bills" data-rule-state="current"`) || !strings.Contains(body, `href="/rules/bills/edit"`) {
		t.Fatalf("decisions page = %d without rule links", status)
	}

	edited := map[string]any{"account": "icloud", "action": "file", "dest": "Receipts", "when": []any{map[string]any{"field": "from_addr", "op": "eq", "value": "billing@shop.test"}}}
	apiData(t, s.do(http.MethodPatch, "/api/v1/mail/rules/bills", token, map[string]any{"rule": edited}), http.StatusOK)
	apiData(t, s.do(http.MethodDelete, "/api/v1/mail/rules/promo", token, nil), http.StatusOK)

	got := triageRules()
	if bill := got["Bill 1"]; bill["state"] != "changed" || bill["title"] != "File into Receipts" {
		t.Fatalf("a decision by an edited rule shows %v", bill)
	}
	if sale := got["Big sale"]; sale["state"] != "removed" || sale["id"] != "promo" || sale["title"] != nil {
		t.Fatalf("a decision by a removed rule shows %v", sale)
	}
	removed := apiData(t, s.do(http.MethodGet, "/api/v1/mail/rules/promo/decisions", token, nil), http.StatusOK)
	if ref := removed["rule"].(map[string]any); ref["state"] != "removed" || removed["total"] != float64(1) || len(apiItems(t, removed)) != 1 {
		t.Fatalf("a removed rule lists %v", removed)
	}
	edit := apiData(t, s.do(http.MethodGet, decisions, token, nil), http.StatusOK)
	if ref := edit["rule"].(map[string]any); ref["state"] != "current" || ref["title"] != "File into Receipts" {
		t.Fatalf("an edited rule reads as %v", ref)
	}
	for _, item := range apiItems(t, edit) {
		if ref := item.(map[string]any)["rule"].(map[string]any); ref["state"] != "changed" {
			t.Fatalf("a decision made before the edit shows %v", ref)
		}
	}

	status, body, _ = fetch(t, http.DefaultClient, http.MethodGet, base+"/rules/bills/edit", nil)
	if status != http.StatusOK || !strings.Contains(body, `data-rule-decisions="3"`) || strings.Count(body, "Rule changed since") != 3 {
		t.Fatalf("rule edit page = %d without its decisions", status)
	}
	status, body, _ = fetch(t, http.DefaultClient, http.MethodGet, base+"/triage?stage=operator", nil)
	if status != http.StatusOK || !strings.Contains(body, `data-rule-ref="promo" data-rule-state="removed"`) || !strings.Contains(body, "Rule removed") || !strings.Contains(body, "Rule changed") {
		t.Fatalf("decisions page = %d without rule states", status)
	}

	if got := count("SELECT COUNT(*) FROM decisions"); got != beforeDecisions || count("SELECT COUNT(*) FROM rules") != beforeRules-1 {
		t.Fatalf("reads changed stored decisions: %d -> %d", beforeDecisions, got)
	}
}
