package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
)

type pinSet map[string]string

func (p pinSet) Pin(key string) (string, string, bool) {
	v, ok := p[key]
	return v, "file", ok
}

func runSecrets(t *testing.T, stdin string, args ...string) settingsRun {
	t.Helper()
	var out, errs bytes.Buffer
	app := newApp()
	app.Stdin = strings.NewReader(stdin)
	code := app.Run(context.Background(), append([]string{"secrets"}, args...), &out, &errs)
	return settingsRun{code, out.String(), errs.String()}
}

func TestSecretsCommandsStoreWriteOnlyAndRefuseFlaggedSecretsOnPlaintext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	t.Setenv("ATRIUM_DB", path)
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))

	const value = "sk-test-0123456789-never-printed"
	set := runSecrets(t, value+"\n", "set", secrets.ActionLinkKey)
	if set.code != cli.ExitOK || set.stdout != secrets.ActionLinkKey+": set\n" || set.stderr != "" {
		t.Fatalf("secrets set: exit %d stdout %q stderr %q", set.code, set.stdout, set.stderr)
	}
	argv := runSecrets(t, "", "set", secrets.ActionLinkKey, value)
	if argv.code != cli.ExitUsage || strings.Contains(argv.stdout+argv.stderr, value) || !strings.Contains(argv.stderr, "must come from standard input") {
		t.Errorf("set with the value in argv: exit %d stdout %q stderr %q", argv.code, argv.stdout, argv.stderr)
	}
	status := runSecrets(t, "", "status")
	for _, want := range []string{"action_link_key: set\n", "anthropic_admin_key: unset\n", "openai_admin_key: unset\n"} {
		if !strings.Contains(status.stdout, want) {
			t.Errorf("status = %q, want %q", status.stdout, want)
		}
	}
	for _, r := range []settingsRun{set, status, runSecrets(t, "", "set", "monthly_budget_usd")} {
		if strings.Contains(r.stdout, value) || strings.Contains(r.stderr, value) {
			t.Errorf("the secret value appeared in output: %q %q", r.stdout, r.stderr)
		}
	}

	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var stored string
	var versions int
	if err := d.Read.QueryRow("SELECT value FROM secrets WHERE name = ?", secrets.ActionLinkKey).Scan(&stored); err != nil || stored != value {
		t.Fatalf("stored secret = %q, %v", stored, err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = 3").Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("migration 3 rows = %d, %v", versions, err)
	}

	if del := runSecrets(t, "", "delete", secrets.ActionLinkKey); del.code != cli.ExitOK || del.stdout != "action_link_key: unset\n" {
		t.Fatalf("secrets delete: exit %d stdout %q stderr %q", del.code, del.stdout, del.stderr)
	}

	refused := runSecrets(t, value, "set", secrets.AnthropicAdminKey)
	want := "error: " + secrets.EncryptionRequired(secrets.AnthropicAdminKey).Error() + "\n"
	if refused.code != cli.ExitError || refused.stdout != "" || refused.stderr != want || !strings.Contains(want, "encrypt the database first") {
		t.Fatalf("flagged set on plaintext: exit %d stdout %q stderr %q, want %q", refused.code, refused.stdout, refused.stderr, want)
	}
	var rows int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM secrets").Scan(&rows); err != nil || rows != 0 {
		t.Errorf("secrets rows after refusal and delete = %d, %v; want 0", rows, err)
	}

	settings.Default.SetPins(pinSet{secrets.ActionLinkKey: "pinned-value"})
	t.Cleanup(func() { settings.Default.SetPins(nil) })
	if _, err := d.Write.Exec("INSERT INTO secrets (name, value, set_at, set_by) VALUES (?, 'stale', 1.0, 'cli')", secrets.ActionLinkKey); err != nil {
		t.Fatal(err)
	}
	pinned := runSecrets(t, value, "set", secrets.ActionLinkKey)
	if pinned.code != cli.ExitError || !strings.Contains(pinned.stderr, "pinned by file") || strings.Contains(pinned.stderr, value) {
		t.Errorf("set on a pinned secret: exit %d stderr %q", pinned.code, pinned.stderr)
	}
	if st := runSecrets(t, "", "status"); !strings.Contains(st.stdout, "action_link_key: pinned\n") {
		t.Errorf("status = %q, want action_link_key pinned", st.stdout)
	}
	if del := runSecrets(t, "", "delete", secrets.ActionLinkKey); del.code != cli.ExitOK || del.stdout != "action_link_key: pinned\n" {
		t.Errorf("delete of a stale row under a pin: exit %d stdout %q stderr %q", del.code, del.stdout, del.stderr)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM secrets").Scan(&rows); err != nil || rows != 0 {
		t.Errorf("secrets rows after pinned delete = %d, %v; want 0", rows, err)
	}
}
