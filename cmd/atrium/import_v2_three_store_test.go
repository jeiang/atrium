//go:build !modernc

package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func TestImportV2ThreeStoresAccountForEveryRowAndPin(t *testing.T) {
	t.Parallel()
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	for _, statement := range []string{
		"UPDATE settings SET value = 'low' WHERE key = 'default_effort'",
		"INSERT INTO settings (key, value) VALUES ('public_url', 'https://shadowed.example')",
		"INSERT INTO settings (key, value) VALUES ('mail_embed_batch', '4')",
		"INSERT INTO settings (key, value) VALUES ('assistant_enabled', 'true')",
		"INSERT INTO settings (key, value) VALUES ('oidc_access_group', 'legacy-group')",
		"INSERT INTO settings (key, value) VALUES ('not_a_real_key', 'unknown-value')",
	} {
		editImportSource(t, source, statement)
	}
	before := fileDigest(t, source)
	configPath := filepath.Join(dir, "v2.toml")
	if err := os.WriteFile(configPath, []byte("[assistant]\ndefault_effort = \"xhigh\"\n[core]\npublic_url = \"https://pin.example\"\n[mail]\nmail_chat_ca_file = \"/operator/chat-ca.pem\"\n[grafana]\ngrafana_ca_file = \"/operator/grafana-ca.pem\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(dir, "v2.env")
	if err := os.WriteFile(envPath, []byte("ATRIUM_MAIL_MAIL_EMBED_BATCH=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := runImportApp(t, importTestApp(t, dir, clock),
		"db", "import-v2", source, "--source-config", configPath, "--source-env", envPath)
	if got.code != cli.ExitOK {
		t.Fatalf("db import-v2: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if after := fileDigest(t, source); after != before {
		t.Errorf("the import changed the source: %s -> %s", before, after)
	}
	for _, want := range []string{
		"setting default_effort: carried -> settings.default_effort from pin config " + configPath + " assistant.default_effort",
		"setting public_url: drop-host -> host.core.public_url; host requirement host.core.public_url = https://pin.example (pin config " + configPath + " core.public_url)",
		"setting mail_embed_batch: drop-tuning -> internal/endpoint/tuning.go:TuningMailEmbedBatch; pin source env " + envPath + " ATRIUM_MAIL_MAIL_EMBED_BATCH value 1 differs from the compiled default 16 (changed-from-default)",
		"pin mail_chat_ca_file: dropped -> the service/anthropic connection and monthly_budget_usd; mail classification uses fixed-schema Haiku",
		"pin grafana_ca_file: drop-host -> service/grafana.transport.ca_file; host requirement service/grafana.transport.ca_file = /operator/grafana-ca.pem (pin config " + configPath + " grafana.grafana_ca_file)",
		"setting assistant_enabled: carried -> settings.assistant_enabled; module flag stays an operator setting",
		"setting oidc_access_group: dropped -> host.auth.oidc_access_groups; host requirement host.auth.oidc_access_groups = legacy-group (settings)",
		"setting not_a_real_key: drop-unknown",
		"import totals: carried=",
		"unmapped_operator_choices=0",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the report lacks %q:\n%s", want, got.stdout)
		}
	}
	if strings.Contains(got.stdout+got.stderr, "unknown-value") {
		t.Errorf("the report printed an unknown setting value:\n%s", got.stdout)
	}
	dest, err := db.OpenSource(filepath.Join(dir, "atrium.db"), importKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dest.Close() }()
	for _, tc := range []struct {
		key  string
		want string
	}{
		{"default_effort", "xhigh"},
		{"assistant_enabled", "true"},
	} {
		if value := importValue(t, dest, "SELECT value FROM settings WHERE key = ?", tc.key); value != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, value, tc.want)
		}
	}
	for _, key := range []string{"public_url", "mail_embed_batch", "not_a_real_key"} {
		if n := importCount(t, dest, "SELECT COUNT(*) FROM settings WHERE key = ?", key); n != 0 {
			t.Errorf("%s survived the import", key)
		}
	}
}

func TestImportV2CarriesHistoricalAuditUnchangedAndAppendsImportEntries(t *testing.T) {
	t.Parallel()
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	for _, statement := range []string{
		"INSERT INTO settings_audit (id, ts, key, old_value, new_value, secret, actor, source) VALUES (7, 1700000000.5, 'default_effort', 'medium', 'low', 0, 'operator', 'ui')",
		"INSERT INTO settings_audit (id, ts, key, old_value, new_value, secret, actor, source) VALUES (9, 1700000100.25, 'action_link_key', NULL, 'changed', 1, 'operator', 'cli')",
		"INSERT INTO settings (key, value) VALUES ('mail_mode', 'live')",
	} {
		editImportSource(t, source, statement)
	}
	setEnv(t, "ATRIUM_INITIAL_TZ", "Europe/Paris")
	got := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source)
	if got.code != cli.ExitOK {
		t.Fatalf("db import-v2: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	dest, err := db.Connect(filepath.Join(dir, "atrium.db"), db.WithKey(importKey(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dest.Close() }()
	type auditRow struct {
		id     int64
		ts     float64
		key    string
		old    sql.NullString
		next   sql.NullString
		secret int
		actor  string
		source string
	}
	rows, err := dest.Read.Query("SELECT id, ts, key, old_value, new_value, secret, actor, source FROM settings_audit ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var audit []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.id, &r.ts, &r.key, &r.old, &r.next, &r.secret, &r.actor, &r.source); err != nil {
			t.Fatal(err)
		}
		audit = append(audit, r)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	want := []auditRow{
		{7, 1700000000.5, "default_effort", sql.NullString{String: "medium", Valid: true}, sql.NullString{String: "low", Valid: true}, 0, "operator", "ui"},
		{9, 1700000100.25, "action_link_key", sql.NullString{}, sql.NullString{String: "changed", Valid: true}, 1, "operator", "cli"},
	}
	if len(audit) < len(want) {
		t.Fatalf("the destination has %d audit rows, want at least %d", len(audit), len(want))
	}
	for i, w := range want {
		if audit[i] != w {
			t.Errorf("audit row %d = %+v, want %+v", i, audit[i], w)
		}
	}
	var bootstrap *auditRow
	for i := range audit {
		if audit[i].key == "tz" && audit[i].actor == "bootstrap" {
			bootstrap = &audit[i]
		}
	}
	if bootstrap == nil {
		t.Error("the destination bootstrap audit row is missing")
	} else if bootstrap.id <= 9 {
		t.Errorf("the destination bootstrap audit row id = %d, want it renumbered above the source range", bootstrap.id)
	}
	last := audit[len(audit)-1]
	if last.key != "mail_mode" || last.source != "migration" || last.secret != 0 {
		t.Errorf("the import audit entry = %+v, want a sanitized mail_mode migration entry", last)
	}
	if _, err := dest.Write.Exec("UPDATE settings_audit SET actor = 'x' WHERE id = 7"); err == nil {
		t.Error("settings_audit accepted an update")
	}
	if _, err := dest.Write.Exec("DELETE FROM settings_audit WHERE id = 7"); err == nil {
		t.Error("settings_audit accepted a delete")
	}
}

func TestImportV2PinnedSecretNeverEntersTheDatabase(t *testing.T) {
	t.Parallel()
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	const stored = "stored-secret-sentinel"
	const pinned = "pinned-secret-sentinel"
	editImportSource(t, source, "INSERT INTO secrets (name, value, set_at, set_by) VALUES ('oidc_client_secret', '"+stored+"', 1, 'ui')")
	secretPath := filepath.Join(dir, "oidc-secret")
	if err := os.WriteFile(secretPath, []byte(pinned+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "v2.toml")
	if err := os.WriteFile(configPath, []byte("[auth]\noidc_client_secret_file = \""+secretPath+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source, "--source-config", configPath)
	if got.code != cli.ExitOK {
		t.Fatalf("db import-v2: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if strings.Contains(got.stdout+got.stderr, stored) || strings.Contains(got.stdout+got.stderr, pinned) {
		t.Errorf("the report printed a secret value:\n%s", got.stdout)
	}
	for _, want := range []string{
		"pin oidc_client_secret: credential-file-binding service/oidc host credential client_secret",
		"secret oidc_client_secret: drop-shadowed-row (the effective pin wins)",
		"secret oidc_client_secret: credential-file-binding service/oidc host credential client_secret",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the report lacks %q:\n%s", want, got.stdout)
		}
	}
	dest, err := db.OpenSource(filepath.Join(dir, "atrium.db"), importKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dest.Close() }()
	if n := importCount(t, dest, "SELECT COUNT(*) FROM secrets WHERE name = 'oidc_client_secret'"); n != 0 {
		t.Errorf("%d shadowed secret rows survived the import", n)
	}
	rows, err := dest.Query("SELECT COALESCE(old_value, ''), COALESCE(new_value, '') FROM settings_audit")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var old, next string
		if err := rows.Scan(&old, &next); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(old, stored) || strings.Contains(next, stored) || strings.Contains(old, pinned) || strings.Contains(next, pinned) {
			t.Error("an audit row holds a secret value")
		}
	}
}

func TestImportV2RefusesAnUnknownSecretBeforeCreatingTheDestination(t *testing.T) {
	t.Parallel()
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	const sentinel = "unknown-secret-sentinel"
	editImportSource(t, source, "INSERT INTO secrets (name, value, set_at, set_by) VALUES ('mystery_token', '"+sentinel+"', 1, 'ui')")
	got := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source)
	if got.code == cli.ExitOK {
		t.Fatalf("an unknown secret was imported: %s", got.stdout)
	}
	if !strings.Contains(got.stderr, "mystery_token") {
		t.Errorf("the refusal does not name the secret: %q", got.stderr)
	}
	if strings.Contains(got.stdout+got.stderr, sentinel) {
		t.Errorf("the refusal printed the secret value: %q", got.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "atrium.db")); !os.IsNotExist(err) {
		t.Errorf("the destination exists after a refused import: %v", err)
	}
}

func TestImportV2ResetsEveryEffectiveLiveMailMode(t *testing.T) {
	t.Parallel()
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	for _, tc := range []struct {
		name string
		row  string
		want string
	}{
		{name: "pin only", want: "pin mail_mode: reset-for-cutover live -> shadow from pin config "},
		{name: "pin over shadow row", row: "shadow", want: "setting mail_mode: reset-for-cutover live -> shadow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setEnv(t, "ATRIUM_MAIL_MAIL_MODE", "")
			dir := t.TempDir()
			source := copyV2Fixture(t, dir)
			editImportSource(t, source, "DELETE FROM settings WHERE key = 'mail_mode'")
			if tc.row != "" {
				editImportSource(t, source, "INSERT INTO settings (key, value) VALUES ('mail_mode', '"+tc.row+"')")
			}
			configPath := filepath.Join(dir, "v2.toml")
			if err := os.WriteFile(configPath, []byte("[mail]\nmail_mode = \"live\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			got := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source, "--source-config", configPath)
			if got.code != cli.ExitOK {
				t.Fatalf("db import-v2: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
			}
			if !strings.Contains(got.stdout, tc.want) {
				t.Errorf("the report lacks %q:\n%s", tc.want, got.stdout)
			}
			dest, err := db.OpenSource(filepath.Join(dir, "atrium.db"), importKey(t))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = dest.Close() }()
			if value := importValue(t, dest, "SELECT value FROM settings WHERE key = 'mail_mode'"); value != "shadow" {
				t.Errorf("mail_mode = %q, want shadow", value)
			}
			if n := importCount(t, dest, "SELECT COUNT(*) FROM settings_audit WHERE key = 'mail_mode' AND old_value = 'live' AND new_value = 'shadow'"); n != 1 {
				t.Errorf("the reset audit rows = %d, want 1", n)
			}
		})
	}
}

func TestImportV2RefusesAnAmbiguousICloudOverride(t *testing.T) {
	t.Parallel()
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	editImportSource(t, source, `INSERT INTO mail_accounts (id, label, preset, login, enabled, pinned, auth_method, created_at) VALUES ('icloud2', 'iCloud Two', 'icloud', 'second@icloud.invalid', 1, 0, 'password', 1762000800)`)
	const appleID = "override@example.test"
	configPath := filepath.Join(dir, "v2.toml")
	if err := os.WriteFile(configPath, []byte("[calendar]\nicloud_apple_id = \""+appleID+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source, "--source-config", configPath)
	if got.code == cli.ExitOK {
		t.Fatalf("the import accepted an ambiguous iCloud override: %s", got.stdout)
	}
	for _, want := range []string{"calendar.icloud_apple_id", "calendar_account", "icloud", "icloud2"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the refusal lacks %q: %q", want, got.stderr)
		}
	}
	if strings.Contains(got.stdout+got.stderr, appleID) {
		t.Errorf("the refusal printed the override value: %q", got.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "atrium.db")); !os.IsNotExist(err) {
		t.Errorf("the destination exists after a refused import: %v", err)
	}
}

func TestImportV2NamesTheSourceEnvLineWithoutTheValue(t *testing.T) {
	t.Parallel()
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	const sentinel = "malformed-secret-sentinel"
	envPath := filepath.Join(dir, "v2.env")
	if err := os.WriteFile(envPath, []byte("ATRIUM_MAIL_MAIL_CHAT_KEY: "+sentinel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source, "--source-env", envPath)
	if got.code == cli.ExitOK {
		t.Fatalf("the import accepted a malformed source environment: %s", got.stdout)
	}
	want := "source environment " + envPath + ": line 1 is not NAME=VALUE"
	if !strings.Contains(got.stderr, want) {
		t.Errorf("the refusal lacks %q: %q", want, got.stderr)
	}
	if strings.Contains(got.stdout+got.stderr, sentinel) {
		t.Errorf("the refusal printed the line: %q", got.stderr)
	}
}

func TestImportV2KeepsFileContentsLiteral(t *testing.T) {
	t.Parallel()
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	promptPath := filepath.Join(dir, "prompt.txt")
	const literal = "Keep ${HOME} literal\n"
	if err := os.WriteFile(promptPath, []byte(literal), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "PROMPT_SUBAGENT", "Keep ${HOME} literal")
	configPath := filepath.Join(dir, "v2.toml")
	body := "[assistant]\nprompt_system_file = \"" + promptPath + "\"\nprompt_subagent = \"${PROMPT_SUBAGENT}\"\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source, "--source-config", configPath)
	if got.code != cli.ExitOK {
		t.Fatalf("db import-v2: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	dest, err := db.OpenSource(filepath.Join(dir, "atrium.db"), importKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dest.Close() }()
	for _, key := range []string{"prompt_system", "prompt_subagent"} {
		if value := importValue(t, dest, "SELECT value FROM settings WHERE key = ?", key); value != "Keep ${HOME} literal" {
			t.Errorf("%s = %q, want the literal file and scalar contents", key, value)
		}
	}
}
