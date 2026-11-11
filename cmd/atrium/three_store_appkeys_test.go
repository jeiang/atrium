package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/actionlink"
	"github.com/jeiang/atrium/internal/actionlink/policy"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/settings"
)

func TestAppKeyFilesSignLinksAcrossRestarts(t *testing.T) {
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	actionKey := strings.Repeat("ab", 32)
	linkKey := "app-key-file-link-secret"
	keyDir := filepath.Join(dir, "keys")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	actionPath := filepath.Join(keyDir, "action.key")
	linkPath := filepath.Join(keyDir, "link.key")
	if err := os.WriteFile(actionPath, []byte(actionKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(linkPath, []byte(linkKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hostPath := filepath.Join(dir, "host.toml")
	host, err := os.ReadFile(defaultHostConfig)
	if err != nil {
		t.Fatal(err)
	}
	body := string(host) + "\n[app_keys]\naction_link_key_file = " + strconv.Quote(actionPath) + "\nlink_key_file = " + strconv.Quote(linkPath) + "\n"
	if err := os.WriteFile(hostPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", hostPath)

	if ran := runApp(t, "settings", "list"); ran.code != cli.ExitOK {
		t.Fatalf("settings list: exit %d stderr %q", ran.code, ran.stderr)
	}
	if got := runSettings(t, "set", policy.EnabledKey, "true"); got.code != cli.ExitOK {
		t.Fatalf("enable phone actions: exit %d stderr %q", got.code, got.stderr)
	}

	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	if _, err := d.Write.Exec("INSERT INTO proposals (id, kind, status, created_ts, title, fingerprint) VALUES (1, 'reminder', 'pending', 0, 'Remind me', 'fp-1')"); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(dir, "media")
	rel := "movies/Dune (2021)/Dune (2021).mkv"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, rel), []byte("movie bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := settings.With(context.Background(), f.settings)
	ctx = config.With(ctx, f.state)
	now := float64(time.Now().Unix())
	_, dismiss, err := actionlink.MintPair(ctx, d.Write, 1, "fp-1", now)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := media.NewLinks(f.settings, d, filepath.Join(dir, "retired.key")).PublicURL(ctx, "", rel, now, 3600)
	if err != nil {
		t.Fatal(err)
	}

	_, _, stop := startServeApp(t, freshApp(f, nil), &lockedBuffer{})
	stop()
	base, _, _ := startServeApp(t, freshApp(f, nil), &lockedBuffer{})

	if code, text, _ := fetch(t, http.DefaultClient, http.MethodGet, base+signed, nil); code != http.StatusOK || text != "movie bytes" {
		t.Errorf("GET the media link after the restart = %d %q, want the signed file", code, text)
	}
	if code, text, _ := fetch(t, http.DefaultClient, http.MethodPost, base+"/actions/"+dismiss, nil); code != http.StatusOK || !strings.Contains(text, "handled from the phone") {
		t.Errorf("POST the action link after the restart = %d %q, want a redeemed link", code, text)
	}

	var status string
	if err := d.Read.QueryRow("SELECT status FROM proposals WHERE id = 1").Scan(&status); err != nil || status != "dismissed" {
		t.Errorf("proposal status = %q, %v, want dismissed", status, err)
	}
	for _, name := range []string{"action_link_key", "link_key"} {
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM secrets WHERE name = ?", name).Scan(&n); err != nil || n != 0 {
			t.Errorf("secrets holds %s: %d, %v; the host file key must not be copied", name, n, err)
		}
	}
	for _, want := range []string{actionKey, linkKey} {
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM secrets WHERE value LIKE ?", "%"+want+"%").Scan(&n); err != nil || n != 0 {
			t.Errorf("a secret row holds the host file key bytes: %d, %v", n, err)
		}
	}
}
