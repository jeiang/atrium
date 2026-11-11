package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
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

	setEnv(t, "ATRIUM_ACTION_LINKS_ACTION_LINK_KEY", "pinned-value")
	if _, err := d.Write.Exec("INSERT INTO secrets (name, value, set_at, set_by) VALUES (?, 'stale', 1.0, 'cli')", secrets.ActionLinkKey); err != nil {
		t.Fatal(err)
	}
	pinned := runSecrets(t, value, "set", secrets.ActionLinkKey)
	if pinned.code != cli.ExitError || !strings.Contains(pinned.stderr, "pinned by env ATRIUM_ACTION_LINKS_ACTION_LINK_KEY") || strings.Contains(pinned.stderr, value) {
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

func TestEmptyPinnedLinkKeyIsRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_CONFIG", "")
	setEnv(t, "ATRIUM_MEDIA_LINK_KEY", "")
	for name, body := range map[string]string{"empty": "", "blank": " \n\t\n"} {
		path := filepath.Join(dir, name+".key")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		setEnv(t, "ATRIUM_MEDIA_LINK_KEY_FILE", path)
		if r := runSecrets(t, "", "status"); r.code != cli.ExitError || !strings.Contains(r.stderr, "ATRIUM_MEDIA_LINK_KEY_FILE") || r.stdout != "" {
			t.Errorf("%s file: exit %d stdout %q stderr %q", name, r.code, r.stdout, r.stderr)
		}
		toml := filepath.Join(dir, name+".toml")
		if err := os.WriteFile(toml, []byte("[media]\nlink_key_file = \""+path+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		setEnv(t, "ATRIUM_MEDIA_LINK_KEY_FILE", "")
		setEnv(t, "ATRIUM_CONFIG", toml)
		if r := runSecrets(t, "", "status"); r.code != cli.ExitError || !strings.Contains(r.stderr, "media.link_key_file") || r.stdout != "" {
			t.Errorf("%s config file: exit %d stdout %q stderr %q", name, r.code, r.stdout, r.stderr)
		}
		setEnv(t, "ATRIUM_CONFIG", "")
	}
}

func TestConnectionsRefuseCredentialsOnAPlaintextDatabase(t *testing.T) {
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	base := startServeIn(t, dir)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if code, _, _ := fetch(t, client, http.MethodGet, base+"/settings/connections/google", nil); code != http.StatusOK {
		t.Fatalf("google page: %d", code)
	}
	const value = "secret-never-printed"
	code, body, _ := fetch(t, client, http.MethodPost, base+"/settings/connections/google",
		url.Values{"client_id": {"client-id"}, "client_secret": {value}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "encrypt the database first") || strings.Contains(body, value) {
		t.Fatalf("google credential on plaintext: %d %s", code, body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	var rows int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM connection_credentials WHERE connection_id = 'google/default'").Scan(&rows); err != nil || rows != 0 {
		t.Errorf("credentials stored on plaintext = %d, %v", rows, err)
	}
}
