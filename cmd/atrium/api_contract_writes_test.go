package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/module"
)

func TestSettingsPatchAppliesAtomicallyWithIfMatch(t *testing.T) {
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
	written := func() (n int) {
		t.Helper()
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM settings_audit").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	value := func(key string) string {
		t.Helper()
		var v string
		if err := d.Read.QueryRow("SELECT COALESCE((SELECT value FROM settings WHERE key = ?), '')", key).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	revision := func() string {
		t.Helper()
		got := s.do(http.MethodGet, "/api/v1/settings?group=assistant", token, nil)
		apiData(t, got, http.StatusOK)
		rev, _ := got.json["revision"].(string)
		if rev == "" || got.header.Get("ETag") != `W/"`+rev+`"` {
			t.Fatalf("settings revision %q, ETag %q", rev, got.header.Get("ETag"))
		}
		if other := s.do(http.MethodGet, "/api/v1/settings?group=export", token, nil); other.json["revision"] != rev {
			t.Fatalf("revision depends on the page: %v vs %s", other.json["revision"], rev)
		}
		return rev
	}

	start := revision()
	before := written()
	patched := s.do(http.MethodPatch, "/api/v1/settings", token, map[string]any{"set": map[string]any{"monthly_budget_usd": "220", "budget_unattended_share": "0.7"}}, "If-Match", `W/"`+start+`"`)
	data := apiData(t, patched, http.StatusOK)
	if keys := fmt.Sprint(data["keys"]); keys != "[budget_unattended_share monthly_budget_usd]" {
		t.Fatalf("patched keys %s", keys)
	}
	next := revision()
	if next == start || data["revision"] != next {
		t.Fatalf("revision %s -> %s, response %v", start, next, data["revision"])
	}
	if value("monthly_budget_usd") != "220" || value("budget_unattended_share") != "0.7" || written()-before != 2 {
		t.Fatalf("patch wrote budget %q share %q, %d audit rows", value("monthly_budget_usd"), value("budget_unattended_share"), written()-before)
	}

	before = written()
	stale := s.do(http.MethodPatch, "/api/v1/settings", token, map[string]any{"set": map[string]any{"monthly_budget_usd": "300"}}, "If-Match", `W/"`+start+`"`)
	expectAPI(t, stale, http.StatusPreconditionFailed, "precondition_failed")
	if stale.json["revision"] != next {
		t.Fatalf("412 carries revision %v, want %s", stale.json["revision"], next)
	}
	if value("monthly_budget_usd") != "220" || written() != before || revision() != next {
		t.Fatal("a stale patch wrote")
	}

	crossed := s.do(http.MethodPatch, "/api/v1/settings", token, map[string]any{"set": map[string]any{"budget_unattended_share": "0.9", "budget_chat_share": "0.85"}}, "If-Match", next)
	expectAPI(t, crossed, http.StatusUnprocessableEntity, "invalid")
	fields, _ := crossed.json["error"].(map[string]any)["fields"].([]any)
	var locs []string
	for _, f := range fields {
		loc, _ := f.(map[string]any)["loc"].([]any)
		locs = append(locs, fmt.Sprint(loc...))
	}
	if !slices.Contains(locs, "bodybudget_unattended_share") && !slices.Contains(locs, "bodybudget_chat_share") {
		t.Fatalf("cross-field failure fields %v: %s", locs, crossed.body)
	}
	if value("budget_unattended_share") != "0.7" || value("budget_chat_share") != "" || written() != before || revision() != next {
		t.Fatal("a cross-field failure wrote")
	}

	expectAPI(t, s.do(http.MethodPatch, "/api/v1/settings", token, map[string]any{"set": map[string]any{"monthly_budget_usd": "400", pinned: "true"}}), http.StatusConflict, "refused")
	if value("monthly_budget_usd") != "220" || written() != before {
		t.Fatal("a patch naming a pinned key wrote")
	}

	off := s.do(http.MethodGet, "/api/v1/mail/rules", token, nil)
	vocabulary := s.do(http.MethodGet, "/api/v1/mail/rules/vocabulary", token, nil)
	if off.status < 400 || vocabulary.status != off.status || vocabulary.code() != off.code() {
		t.Fatalf("vocabulary with Mail off: %d %s, mail rules: %d %s", vocabulary.status, vocabulary.code(), off.status, off.code())
	}
}

func TestContractSpecPreviewsAndRuleVocabulary(t *testing.T) {
	out := t.TempDir()
	if code, stderr := runCatalogOpenAPI(t, "--output", out); code != 0 {
		t.Fatalf("catalog openapi: %d %s", code, stderr)
	}
	raw, err := os.ReadFile(filepath.Join(out, "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	paths := spec["paths"].(map[string]any)
	op := func(path, method string) map[string]any {
		t.Helper()
		o, _ := paths[path].(map[string]any)[method].(map[string]any)
		if o == nil {
			t.Fatalf("no %s %s", method, path)
		}
		return o
	}
	if op("/approvals/{proposal_id}/approve", "post")["x-atrium-undoable"] != true || op("/tasks", "post")["x-atrium-undoable"] != true {
		t.Fatal("undoable operations are not marked")
	}
	if _, marked := op("/settings", "patch")["x-atrium-undoable"]; marked {
		t.Fatal("settings patch is marked undoable")
	}
	if _, marked := op("/mail/scans/rebalance", "post")["x-atrium-undoable"]; marked {
		t.Fatal("a scan is marked undoable")
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)

	davFixture(t)
	notes := strings.TrimSpace(strings.Repeat("Bring the referral letter and the insurance card. ", 40))
	id := proposalID(t, toolResult(t, "calendar_create", map[string]any{"calendar": "icloud", "title": "Dentist", "start": "2026-11-10T12:00:00", "end": "2026-11-10T13:00:00", "notes": notes}))
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}

	detail := apiData(t, s.do(http.MethodGet, "/api/v1/proposals/"+id, token, nil), http.StatusOK)
	change, _ := detail["payload"].(map[string]any)["change"].(map[string]any)
	if change == nil || change["kind"] != "calendar_event" || change["notes"] != notes {
		t.Fatalf("typed preview: %v", detail["payload"])
	}
	validateSchema(t, schemas, object(schemas["ProposalChange"]), change, "change")
	vocabulary := apiData(t, s.do(http.MethodGet, "/api/v1/mail/rules/vocabulary", token, nil), http.StatusOK)
	validateSchema(t, schemas, object(schemas["RuleVocabulary"]), vocabulary, "vocabulary")
	names := func(key string) []string {
		var out []string
		for _, it := range vocabulary[key].([]any) {
			out = append(out, it.(map[string]any)["name"].(string))
		}
		return out
	}
	if fields := names("fields"); !slices.Contains(fields, "from_addr") || !slices.Contains(fields, "age_days") || !slices.Contains(fields, "att_name") {
		t.Fatalf("fields %v", fields)
	}
	if ops := names("ops"); fmt.Sprint(ops) != "[eq in suffix re present absent gte]" {
		t.Fatalf("ops %v", ops)
	}
	if actions := names("actions"); fmt.Sprint(actions) != "[file delete]" {
		t.Fatalf("actions %v", actions)
	}
}

func object(v any) map[string]any {
	o, _ := v.(map[string]any)
	return o
}

func validateSchema(t *testing.T, schemas, schema map[string]any, v any, at string) {
	t.Helper()
	if ref, ok := schema["$ref"].(string); ok {
		validateSchema(t, schemas, object(schemas[strings.TrimPrefix(ref, "#/components/schemas/")]), v, at)
		return
	}
	if alts, ok := schema["anyOf"].([]any); ok {
		if v == nil {
			return
		}
		validateSchema(t, schemas, object(alts[0]), v, at)
		return
	}
	if one, ok := schema["oneOf"].([]any); ok {
		kind, _ := object(v)["kind"].(string)
		ref, _ := object(schema["discriminator"])["mapping"].(map[string]any)[kind].(string)
		if ref == "" {
			t.Fatalf("%s: kind %q is not in the union (%d variants)", at, kind, len(one))
		}
		validateSchema(t, schemas, map[string]any{"$ref": ref}, v, at)
		return
	}
	if c, ok := schema["const"]; ok && c != v {
		t.Fatalf("%s = %v, want %v", at, v, c)
	}
	switch schema["type"] {
	case "object":
		o, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%s is not an object: %v", at, v)
		}
		required, _ := schema["required"].([]any)
		for _, r := range required {
			if _, ok := o[r.(string)]; !ok {
				t.Fatalf("%s lacks required %s", at, r)
			}
		}
		for key, sub := range object(schema["properties"]) {
			if val, ok := o[key]; ok {
				validateSchema(t, schemas, object(sub), val, at+"."+key)
			}
		}
	case "array":
		items, ok := v.([]any)
		if !ok {
			t.Fatalf("%s is not an array: %v", at, v)
		}
		for i, it := range items {
			validateSchema(t, schemas, object(schema["items"]), it, fmt.Sprintf("%s[%d]", at, i))
		}
	case "string":
		if _, ok := v.(string); !ok {
			t.Fatalf("%s is not a string: %v", at, v)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			t.Fatalf("%s is not a boolean: %v", at, v)
		}
	case "integer", "number":
		if _, ok := v.(float64); !ok {
			t.Fatalf("%s is not a number: %v", at, v)
		}
	}
}
