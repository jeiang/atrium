package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/secrets"
)

func runSecrets(t *testing.T, stdin string, args ...string) settingsRun {
	t.Helper()
	var out, errs bytes.Buffer
	app := defaultApp(t)
	app.Stdin = strings.NewReader(stdin)
	code := app.Run(context.Background(), append([]string{"secrets"}, args...), &out, &errs)
	return settingsRun{code, out.String(), errs.String()}
}

func TestSecretsCommandsStoreWriteOnlyAndRefuseFlaggedSecretsOnPlaintext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", path)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))

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
	for _, want := range []string{"action_link_key: set\n"} {
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
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = 1").Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("baseline migration rows = %d, %v", versions, err)
	}

	if del := runSecrets(t, "", "delete", secrets.ActionLinkKey); del.code != cli.ExitOK || del.stdout != "action_link_key: unset\n" {
		t.Fatalf("secrets delete: exit %d stdout %q stderr %q", del.code, del.stdout, del.stderr)
	}

	var rows int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM secrets").Scan(&rows); err != nil || rows != 0 {
		t.Errorf("secrets rows after delete = %d, %v; want 0", rows, err)
	}

}

func TestSecretsRefuseAndReportAFileBackedAppKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", path)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	keyPath := filepath.Join(dir, "action-link.key")
	const value = "file-backed-value-never-printed"
	if err := os.WriteFile(keyPath, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(dir, "host.toml")
	if err := os.WriteFile(host, []byte("[app_keys]\naction_link_key_file = "+strconv.Quote(keyPath)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", host)

	if st := runSecrets(t, "", "status"); !strings.Contains(st.stdout, "action_link_key: file\n") {
		t.Errorf("status = %q, want action_link_key file", st.stdout)
	}
	set := runSecrets(t, value+"\n", "set", secrets.ActionLinkKey)
	if set.code != cli.ExitError || !strings.Contains(set.stderr, "app_keys.action_link_key_file") || strings.Contains(set.stdout+set.stderr, value) {
		t.Errorf("set on a file-backed app key: exit %d stdout %q stderr %q", set.code, set.stdout, set.stderr)
	}

	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec("INSERT INTO secrets (name, value, set_at, set_by) VALUES (?, 'stale', 1.0, 'cli')", secrets.ActionLinkKey); err != nil {
		t.Fatal(err)
	}
	if del := runSecrets(t, "", "delete", secrets.ActionLinkKey); del.code != cli.ExitOK || del.stdout != "action_link_key: file\n" {
		t.Errorf("delete of a stale row under an app key file: exit %d stdout %q stderr %q", del.code, del.stdout, del.stderr)
	}
	var rows int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM secrets WHERE name = ?", secrets.ActionLinkKey).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("secrets rows after delete = %d, %v; want 0", rows, err)
	}
}

func TestRotateRefusesWhenAnAppKeyFileSuppliesTheKey(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	keyPath := filepath.Join(dir, "action-link.key")
	if err := os.WriteFile(keyPath, []byte("file-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", hostConfigAppend(t, defaultHostConfig, dir, "host-app-key.toml",
		"\n[app_keys]\naction_link_key_file = "+strconv.Quote(keyPath)+"\n"))
	base, _, _ := startServeStoppable(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	if code, _, _ := fetch(t, client, http.MethodGet, base+"/settings/notifications", nil); code != http.StatusOK {
		t.Fatalf("notifications page = %d", code)
	}
	code, body, _ := fetch(t, client, http.MethodPost, base+"/settings/phone-actions/revoke", url.Values{})
	if code != http.StatusInternalServerError || !strings.Contains(body, "app_keys.action_link_key_file") {
		t.Fatalf("rotate with an app key file: %d %q", code, body)
	}
}

func TestEmptyAppKeyFileIsRefusedAtStartup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	for name, body := range map[string]string{"empty": "", "blank": " \n\t\n"} {
		path := filepath.Join(dir, name+".key")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		host := filepath.Join(dir, name+".toml")
		if err := os.WriteFile(host, []byte("[app_keys]\nlink_key_file = "+strconv.Quote(path)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		setEnv(t, "ATRIUM_HOST_CONFIG", host)
		if r := runSecrets(t, "", "status"); r.code != cli.ExitError || !strings.Contains(r.stderr, "link_key_file") || r.stdout != "" {
			t.Errorf("%s file: exit %d stdout %q stderr %q", name, r.code, r.stdout, r.stderr)
		}
	}
}
