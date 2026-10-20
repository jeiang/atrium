package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/secrets"
)

type auditRow struct {
	key, old, next, actor, source string
	secret                        bool
}

func TestSettingsAuditRecordsCommandsAndShowsInActivity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	t.Setenv("ATRIUM_DB", path)
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_PUBLIC_HOST", "")

	const first, second = "sk-audit-first-0123456789", "sk-audit-second-9876543210"
	runs := []settingsRun{
		runSettings(t, "set", "monthly_budget_usd", "200"),
		runSettings(t, "set", "monthly_budget_usd", "200"),
		runSettings(t, "set", "monthly_budget_usd", "300"),
		runSettings(t, "unset", "monthly_budget_usd"),
		runSettings(t, "unset", "monthly_budget_usd"),
		runSecrets(t, first+"\n", "set", secrets.ActionLinkKey),
		runSecrets(t, second+"\n", "set", secrets.ActionLinkKey),
		runSecrets(t, "", "delete", secrets.ActionLinkKey),
		runSecrets(t, "", "delete", secrets.ActionLinkKey),
	}
	for i, r := range runs {
		if r.code != cli.ExitOK {
			t.Fatalf("run %d: exit %d stdout %q stderr %q", i, r.code, r.stdout, r.stderr)
		}
	}

	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	rows, err := d.Read.Query("SELECT key, old_value, new_value, actor, source, secret FROM settings_audit ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var got []auditRow
	var dump strings.Builder
	for rows.Next() {
		var r auditRow
		var old, next *string
		if err := rows.Scan(&r.key, &old, &next, &r.actor, &r.source, &r.secret); err != nil {
			t.Fatal(err)
		}
		r.old, r.next = deref(old), deref(next)
		got = append(got, r)
		dump.WriteString(r.key + "|" + r.old + "|" + r.next + "|" + r.actor + "|" + r.source + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []auditRow{
		{"monthly_budget_usd", "", "200", "operator", "cli", false},
		{"monthly_budget_usd", "200", "300", "operator", "cli", false},
		{"monthly_budget_usd", "300", "", "operator", "cli", false},
		{secrets.ActionLinkKey, "", "changed", "operator", "cli", true},
		{secrets.ActionLinkKey, "", "changed", "operator", "cli", true},
		{secrets.ActionLinkKey, "", "changed", "operator", "cli", true},
	}
	if len(got) != len(want) {
		t.Fatalf("audit rows:\n%s\nwant %d rows", dump.String(), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("audit row %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	for _, stmt := range []string{
		"UPDATE settings_audit SET new_value = 'x'",
		"DELETE FROM settings_audit",
	} {
		if _, err := d.Write.Exec(stmt); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: err = %v, want append-only refusal", stmt, err)
		}
	}

	var out strings.Builder
	for _, r := range runs {
		out.WriteString(r.stdout + r.stderr)
	}
	out.WriteString(dump.String())

	base := startServeIn(t, dir)
	status, body, _ := fetch(t, http.DefaultClient, "GET", base+"/activity?category=settings", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /activity?category=settings = %d: %.300s", status, body)
	}
	for _, want := range []string{"monthly_budget_usd", secrets.ActionLinkKey, "by operator via cli", ">200<", ">300<", ">changed<"} {
		if !strings.Contains(body, want) {
			t.Errorf("settings activity lacks %q", want)
		}
	}
	status, body, _ = fetch(t, http.DefaultClient, "GET", base+"/activity", nil)
	if status != http.StatusOK || !strings.Contains(body, "by operator via cli") {
		t.Errorf("GET /activity = %d without the settings rows", status)
	}
	out.WriteString(body)
	for _, secret := range []string{first, second} {
		if strings.Contains(out.String(), secret) {
			t.Errorf("secret value %q appeared in command output, audit rows or /activity", secret)
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func TestStoredSecretsAreRedactedFromLogsAndAgentWrites(t *testing.T) {
	const secret = "sk-redact-money-0123456789"
	extraction := `{"kind": "purchase", "payee_raw": "` + secret + `", "amount": 10, "currency": "TTD", "date": "", "bank": "", "account_last4": "", "account_amount": null, "account_currency": "", "statement_total": null, "description": "` + secret + `", "subscription": false, "cadence": "unknown"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		content := extraction
		if strings.Contains(string(body), "Descriptor:") {
			content = "not json"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	t.Cleanup(srv.Close)
	r := newMoneyRig(t, srv.URL+"/v1")

	if set := runSecrets(t, secret+"\n", "set", secrets.ActionLinkKey); set.code != cli.ExitOK {
		t.Fatalf("secrets set: exit %d stderr %q", set.code, set.stderr)
	}
	var out, logs bytes.Buffer
	if code := r.app.Run(context.Background(), []string{"money", "refresh", "--limit", "1"}, &out, &logs); code != cli.ExitOK {
		t.Fatalf("money refresh exited %d: %s", code, logs.String())
	}
	if !strings.Contains(logs.String(), "payee suggestion failed") || !strings.Contains(logs.String(), logx.Redacted) {
		t.Errorf("log stream lacks the redacted payee warning: %q", logs.String())
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(out.String(), secret) {
		t.Errorf("the secret reached the log stream or stdout: %q", logs.String())
	}
	var after string
	if err := r.d.Read.QueryRow("SELECT after_json FROM agent_writes WHERE tool = 'money_post'").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(after, secret) || !strings.Contains(after, logx.Redacted) {
		t.Errorf("agent_writes snapshot = %q, want the secret replaced by %s", after, logx.Redacted)
	}
}
