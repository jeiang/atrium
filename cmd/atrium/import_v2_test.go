//go:build !modernc

package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

func v2Fixture(t *testing.T) string {
	t.Helper()
	return filepath.Join(golden.Dir(), "..", "v2", "atrium.db")
}

func importTestApp(t *testing.T, dir string, now float64) *cli.App {
	t.Helper()
	return importTestAppWithHost(t, dir, now, "http://127.0.0.1:9", "http://127.0.0.1:9", "http://127.0.0.1:9")
}

func importTestAppWithHost(t *testing.T, dir string, now float64, anthropicURL, grafanaURL, notionURL string) *cli.App {
	t.Helper()
	return importTestAppHost(t, dir, now, importHostConfig(t, dir, anthropicURL, grafanaURL, notionURL))
}

func importTestAppHost(t *testing.T, dir string, now float64, host string) *cli.App {
	t.Helper()
	vars := varsFor(t).snapshot()
	vars["ATRIUM_DB"] = filepath.Join(dir, "atrium.db")
	vars["ATRIUM_CACHE_DIR"] = filepath.Join(dir, "cache")
	vars["ATRIUM_DB_KEY"] = rightKeyHex
	vars["ATRIUM_HOST_CONFIG"] = host
	vars[settingEnv(config.KeyMailAccountsFile)] = defaultAccountsFile
	reg := settings.New()
	t.Cleanup(func() { db.SetDefaultKey(db.Key{}) })
	return newAppWith(config.Map(vars), reg, config.NewState(reg), nil, nil, func() float64 { return now }, quietLog)
}

func importHostConfig(t *testing.T, dir, anthropicURL, grafanaURL, notionURL string) string {
	t.Helper()
	body := fmt.Sprintf(`[connections."service/anthropic"]
kind = "service/anthropic"
label = "Anthropic"
config = { url = %q }
capabilities = ["models"]

[connections."service/grafana"]
kind = "service/grafana"
label = "Grafana"
config = { url = %q }
capabilities = ["viewer"]

[connections."service/notion"]
kind = "service/notion"
label = "Notion"
config = { api = %q }
capabilities = ["api"]
`, anthropicURL, grafanaURL, notionURL)
	path := filepath.Join(dir, "host.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func importKey(t *testing.T) db.Key {
	t.Helper()
	key, err := db.ParseKey(rightKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func runImportApp(t *testing.T, app *cli.App, args ...string) appRun {
	t.Helper()
	var out, errs bytes.Buffer
	code := app.Run(context.Background(), args, &out, &errs)
	return appRun{code, out.String(), errs.String()}
}

func importCount(t *testing.T, q db.Querier, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := q.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func importValue(t *testing.T, q db.Querier, query string, args ...any) string {
	t.Helper()
	var value sql.NullString
	if err := q.QueryRowContext(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return value.String
}

func importNull(t *testing.T, q db.Querier, query string, args ...any) bool {
	t.Helper()
	var value sql.NullString
	if err := q.QueryRowContext(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return !value.Valid
}

func copyV2Fixture(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "v2.db")
	copyFile(t, v2Fixture(t), path)
	return path
}

func TestImportV2CarriesTheDatabaseAndReplayMatches(t *testing.T) {
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	before := fileDigest(t, source)
	dest := filepath.Join(dir, "atrium.db")

	got := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source)
	if got.code != cli.ExitOK {
		t.Fatalf("db import-v2: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if after := fileDigest(t, source); after != before {
		t.Errorf("the import changed the source: %s -> %s", before, after)
	}
	for _, want := range []string{
		"messages: carried 158",
		"message_fts: rebuilt 158",
		"thread_fts: rebuilt 24",
		"kb_fts: rebuilt 3",
		"embed_index: rebuilt 1",
		"embed_message_0: carried 148",
		"usage: carried 22",
		"auto_actions: converted 10",
		"subagents: carried 1",
		"runs: dropped 8",
		"job_runs: dropped 2",
		"schema_migrations: dropped 11",
		"setting money_since: dropped",
		"connection service/anthropic: created from host settings",
		"connection service/anthropic: credential api_key carried",
		"connection service/weather: created from host settings",
		"connection service/webfetch: created from host settings",
		"connection service/tmdb: created from host settings",
		"connection service/eztv: created from host settings",
		"connection service/webpush: created from host settings",
		`oidc client secret: no source secret; set connections."service/oidc".credential_files.client_secret in the host configuration`,
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the report lacks %q:\n%s", want, got.stdout)
		}
	}

	q, err := db.OpenSource(dest, importKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()

	for _, tc := range []struct {
		name  string
		query string
		want  int64
	}{
		{"messages", "SELECT COUNT(*) FROM messages", 158},
		{"message fts", "SELECT COUNT(*) FROM message_fts", 158},
		{"thread messages", "SELECT COUNT(*) FROM thread_messages WHERE thread_id = 2", 23},
		{"thread fts", "SELECT COUNT(*) FROM thread_fts", 24},
		{"kb records", "SELECT COUNT(*) FROM kb_records", 3},
		{"kb fts", "SELECT COUNT(*) FROM kb_fts", 3},
		{"vectors", "SELECT COUNT(*) FROM embed_message_0", 148},
		{"usage", "SELECT COUNT(*) FROM usage", 22},
		{"runs", "SELECT COUNT(*) FROM runs", 0},
		{"subagents", "SELECT COUNT(*) FROM subagents", 4},
		{"operator subagents", "SELECT COUNT(*) FROM subagents WHERE builtin = 0", 1},
		{"parent threads", "SELECT COUNT(*) FROM threads WHERE parent_thread_id = 2", 1},
	} {
		if n := importCount(t, q, tc.query); n != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, n, tc.want)
		}
	}
	if got := importValue(t, q, "SELECT subject FROM messages WHERE id = 85"); got != "Lunch next week" {
		t.Errorf("message 85 subject %q", got)
	}
	if got := importValue(t, q, "SELECT folder FROM corrections WHERE message_id = 93"); got != "Bills" {
		t.Errorf("correction for message 93 folder %q", got)
	}
	if got := importValue(t, q, "SELECT stage FROM decisions WHERE id = 1"); got != "knn" {
		t.Errorf("decision 1 stage %q", got)
	}
	autoEffect := func(id int64) int64 {
		t.Helper()
		var effect int64
		if err := q.QueryRow("SELECT effect_id FROM effect_import_refs WHERE source_table = 'auto_actions' AND source_id = ? AND role = 'forward'", id).Scan(&effect); err != nil {
			t.Fatalf("auto_actions %d has no effect: %v", id, err)
		}
		return effect
	}
	if got := importValue(t, q, "SELECT state FROM effects WHERE id = ?", autoEffect(5)); got != "done" {
		t.Errorf("auto action 5 state %q", got)
	}
	if got := importValue(t, q, "SELECT undo_json FROM effects WHERE id = ?", autoEffect(5)); !strings.Contains(got, "move_back") {
		t.Errorf("auto action 5 undo data %q", got)
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM effects WHERE id = ? AND undone_by IS NOT NULL", autoEffect(5)); n != 1 {
		t.Errorf("auto action 5 undone_by %d, want set", n)
	}
	if got := importValue(t, q, "SELECT dest FROM rules WHERE id = 'club-news'"); got != "Newsletters" {
		t.Errorf("rule club-news dest %q", got)
	}
	if got := importValue(t, q, "SELECT value FROM settings WHERE key = 'default_effort'"); got != "medium" {
		t.Errorf("default_effort %q", got)
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM secrets WHERE name = 'anthropic_api_key'"); n != 0 {
		t.Errorf("the Anthropic key stayed in the secrets table")
	}
	if got := importValue(t, q, "SELECT value FROM connection_credentials WHERE connection_id = 'service/anthropic' AND slot = 'api_key'"); got != "sk-ant-fixture" {
		t.Errorf("the imported Anthropic credential is %q", got)
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM kb_records WHERE embedded_hash IS NOT NULL"); n != 0 {
		t.Errorf("%d knowledge records kept an embedded hash", n)
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM messages WHERE id IN (SELECT rowid FROM embed_message_0) AND embedded_hash IS NULL"); n != 0 {
		t.Errorf("%d carried message vectors have no embedded hash", n)
	}
	if got := importValue(t, q, "SELECT name FROM media_channels WHERE id = 1"); got != "Lanterns" {
		t.Errorf("media channel 1 %q", got)
	}
	if got := importValue(t, q, "SELECT title FROM tasks WHERE id = 1"); got != "Book the passport appointment" {
		t.Errorf("task 1 %q", got)
	}
	if got := importValue(t, q, "SELECT status FROM drafts WHERE id = 1"); got != "draft" {
		t.Errorf("draft 1 status %q", got)
	}
	if !importNull(t, q, "SELECT run_id FROM thread_messages WHERE thread_id = 2 AND seq = 1") {
		t.Error("thread_messages.run_id was not cleared")
	}
	if !importNull(t, q, "SELECT run_id FROM usage WHERE id = 1") {
		t.Error("usage.run_id was not cleared")
	}
	if n := importCount(t, q, "SELECT thread_id FROM usage WHERE id = 1"); n != 1 {
		t.Errorf("usage 1 thread_id %d", n)
	}
	const agentEffect = "SELECT effect_id FROM effect_import_refs WHERE source_table = 'agent_writes' AND source_id = 7 AND role = 'forward'"
	if !importNull(t, q, "SELECT run_id FROM effects WHERE id = ("+agentEffect+")") {
		t.Error("agent_writes.run_id was not cleared")
	}
	if n := importCount(t, q, "SELECT thread_id FROM effects WHERE id = ("+agentEffect+")"); n != 2 {
		t.Errorf("agent_writes 7 thread_id %d", n)
	}
	if !importNull(t, q, "SELECT last_run_id FROM jobs WHERE id = 1") {
		t.Error("jobs.last_run_id was not cleared")
	}

	replay := runImportApp(t, importTestApp(t, dir, clock), "replay")
	if replay.code != cli.ExitOK {
		t.Fatalf("replay: exit %d stdout %q stderr %q", replay.code, replay.stdout, replay.stderr)
	}
	baseline, err := os.ReadFile(filepath.Join(golden.Dir(), "..", "v2", "replay.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if replay.stdout != string(baseline) {
		t.Errorf("the imported copy replays differently from the v2 baseline:\ngot:\n%s\nwant:\n%s", replay.stdout, baseline)
	}

	w, err := db.Connect(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	sent := 0
	fill := func(ctx context.Context, url string, texts []string) ([][]float64, error) {
		sent += len(texts)
		out := make([][]float64, len(texts))
		for i := range out {
			out[i] = make([]float64, config.EmbedDim)
			out[i][0] = 1
		}
		return out, nil
	}
	embedded, err := embed.Pending(context.Background(), w, "http://embed.invalid", fill)
	if err != nil {
		t.Fatal(err)
	}
	if embedded != 10 || sent != 10 {
		t.Errorf("the first fill after import embedded %d messages and sent %d texts, want the 10 without carried vectors", embedded, sent)
	}
	embedded, err = embed.Pending(context.Background(), w, "http://embed.invalid", fill)
	if err != nil {
		t.Fatal(err)
	}
	if embedded != 0 || sent != 10 {
		t.Errorf("the second fill after import embedded %d messages and sent %d texts in total", embedded, sent)
	}

	t.Run("a second import is refused and changes nothing", func(t *testing.T) {
		before := fileDigest(t, source)
		again := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source)
		if again.code == cli.ExitOK || !strings.Contains(again.stderr, "already holds rows") || !strings.Contains(again.stderr, "messages=158") {
			t.Fatalf("second import: exit %d stdout %q stderr %q", again.code, again.stdout, again.stderr)
		}
		if after := fileDigest(t, source); after != before {
			t.Error("the refused import changed the source")
		}
		if n := importCount(t, q, "SELECT COUNT(*) FROM messages"); n != 158 {
			t.Errorf("the refused import changed the destination: messages %d", n)
		}
	})

	t.Run("a source below version 11 is refused", func(t *testing.T) {
		old := copyV2Fixture(t, t.TempDir())
		editImportSource(t, old, "DELETE FROM schema_migrations WHERE version = 11")
		target := t.TempDir()
		got := runImportApp(t, importTestApp(t, target, clock), "db", "import-v2", old)
		if got.code == cli.ExitOK || !strings.Contains(got.stderr, "version 10") || !strings.Contains(got.stderr, "version 11") {
			t.Fatalf("import of a version 10 source: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
		}
		if _, err := os.Stat(filepath.Join(target, "atrium.db")); err == nil {
			t.Error("the refused import created the destination")
		}
	})

	t.Run("an unknown source table is refused by name", func(t *testing.T) {
		extra := copyV2Fixture(t, t.TempDir())
		editImportSource(t, extra, "CREATE TABLE surprise (x INTEGER)")
		target := t.TempDir()
		got := runImportApp(t, importTestApp(t, target, clock), "db", "import-v2", extra)
		if got.code == cli.ExitOK || !strings.Contains(got.stderr, "surprise") {
			t.Fatalf("import with an unknown table: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
		}
		if _, err := os.Stat(filepath.Join(target, "atrium.db")); err == nil {
			t.Error("the refused import created the destination")
		}
	})

	t.Run("a populated destination is refused", func(t *testing.T) {
		target := t.TempDir()
		app := importTestApp(t, target, clock)
		if got := runImportApp(t, app, "settings", "set", "tz", "UTC"); got.code != cli.ExitOK {
			t.Fatalf("settings set: exit %d stderr %q", got.code, got.stderr)
		}
		got := runImportApp(t, app, "db", "import-v2", source)
		if got.code == cli.ExitOK || !strings.Contains(got.stderr, "already holds rows") || !strings.Contains(got.stderr, "settings=1") {
			t.Fatalf("import into a populated destination: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
		}
	})

	t.Run("a source with a send in flight is refused", func(t *testing.T) {
		sending := copyV2Fixture(t, t.TempDir())
		editImportSource(t, sending, "UPDATE drafts SET status = 'sending' WHERE id = 1")
		target := t.TempDir()
		got := runImportApp(t, importTestApp(t, target, clock), "db", "import-v2", sending)
		if got.code == cli.ExitOK || !strings.Contains(got.stderr, "drafts=1") || !strings.Contains(got.stderr, "stop v2") {
			t.Fatalf("import with a send in flight: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
		}
		if _, err := os.Stat(filepath.Join(target, "atrium.db")); err == nil {
			t.Error("the refused import created the destination")
		}
	})

	t.Run("knowledge records are reset for a rebuild", func(t *testing.T) {
		embedded := copyV2Fixture(t, t.TempDir())
		editImportSource(t, embedded, "UPDATE kb_records SET embedded = 1")
		target := t.TempDir()
		got := runImportApp(t, importTestApp(t, target, clock), "db", "import-v2", embedded)
		if got.code != cli.ExitOK {
			t.Fatalf("import: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
		}
		dest, err := db.OpenSource(filepath.Join(target, "atrium.db"), importKey(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = dest.Close() }()
		if n := importCount(t, dest, "SELECT COUNT(*) FROM kb_records WHERE embedded_hash IS NOT NULL"); n != 0 {
			t.Errorf("%d knowledge records kept an embedded hash", n)
		}
	})

	t.Run("usage outside the current month is not carried", func(t *testing.T) {
		old := copyV2Fixture(t, t.TempDir())
		editImportSource(t, old, `INSERT INTO usage (id, ts, request_id, source, provider, thread_id, run_id, kind, job_name, model, effort, iterations, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, reasoning_tokens, searches, tool_calls, wall_ms, tier, cost_usd, partial, unpriced, clear_edits, cleared_tokens, compaction)
			SELECT 99, 1789000000, 'old-month', source, provider, thread_id, run_id, kind, job_name, model, effort, iterations, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, reasoning_tokens, searches, tool_calls, wall_ms, tier, cost_usd, partial, unpriced, clear_edits, cleared_tokens, compaction FROM usage WHERE id = 1`)
		target := t.TempDir()
		got := runImportApp(t, importTestApp(t, target, clock), "db", "import-v2", old)
		if got.code != cli.ExitOK {
			t.Fatalf("import: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
		}
		dest, err := db.OpenSource(filepath.Join(target, "atrium.db"), importKey(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = dest.Close() }()
		if n := importCount(t, dest, "SELECT COUNT(*) FROM usage"); n != 22 {
			t.Errorf("usage rows %d, want the 22 in-month rows", n)
		}
		if n := importCount(t, dest, "SELECT COUNT(*) FROM usage WHERE request_id = 'old-month'"); n != 0 {
			t.Error("the previous month's usage row was carried")
		}
	})
}

func TestImportV2MovesServiceCredentialsIntoConnections(t *testing.T) {
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)

	api := anthropictest.New(t)
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("g1", "grafana_datasources", nil)),
		anthropictest.Reply("end_turn", anthropictest.Text("listed")),
	)
	var mu sync.Mutex
	var apiKeys []string
	var authorizations []string
	api.OnRequest = func(request anthropictest.Request) {
		mu.Lock()
		apiKeys = append(apiKeys, request.Header.Get("X-Api-Key"))
		mu.Unlock()
	}
	grafana := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.URL.Path != "/api/datasources" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[{"uid":"prom","name":"Imported datasource","type":"prometheus"}]`))
	}))
	t.Cleanup(grafana.Close)

	const notionPage = "44444444-4444-4444-4444-444444444444"
	var notionTokens []string
	notionFake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		notionTokens = append(notionTokens, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer ntn-imported-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"object":"error","status":401,"code":"unauthorized","message":"API token is invalid."}`))
			return
		}
		switch r.URL.Path {
		case "/v1/users/me":
			_, _ = w.Write([]byte(`{"object":"user","id":"bot","type":"bot"}`))
		case "/v1/search":
			var body struct {
				Filter struct {
					Value string `json:"value"`
				} `json:"filter"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Filter.Value != "page" {
				_, _ = w.Write([]byte(`{"object":"list","results":[],"has_more":false,"next_cursor":null}`))
				return
			}
			_, _ = w.Write([]byte(`{"object":"list","results":[{"object":"page","id":"` + notionPage + `","url":"https://notion.example/imported","last_edited_time":"2026-01-01T00:00:00.000Z","in_trash":false,"parent":{"type":"workspace"},"properties":{"title":{"type":"title","title":[{"plain_text":"Imported page"}]},"sensitivity":{"type":"formula","formula":{"type":"string","string":"private"}}}}],"has_more":false,"next_cursor":null}`))
		case "/v1/pages/" + notionPage + "/markdown":
			_, _ = w.Write([]byte(`{"markdown":"imported alpha","truncated":false}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(notionFake.Close)

	var tmdbMu sync.Mutex
	var tmdbTokens []string
	tmdbFake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tmdbMu.Lock()
		tmdbTokens = append(tmdbTokens, r.Header.Get("Authorization"))
		tmdbMu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tmdb-imported-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/3/configuration":
			_, _ = w.Write([]byte(`{}`))
		case "/3/search/tv":
			_, _ = w.Write([]byte(`{"results":[{"id":42,"name":"Lanterns","first_air_date":"2026-01-01","poster_path":"/lanterns.jpg"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(tmdbFake.Close)
	tmdbImages := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("image"))
	}))
	t.Cleanup(tmdbImages.Close)
	eztvFake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"torrents":[]}`))
	}))
	t.Cleanup(eztvFake.Close)

	editImportSource(t, source, fmt.Sprintf(`INSERT INTO settings (key, value) VALUES
		('grafana_url', %[1]q),
		('grafana_metrics_uid', 'prom'),
		('weather_open_meteo_url', 'http://weather.invalid/forecast'),
		('weather_geocoding_url', 'http://weather.invalid/search')`, grafana.URL))
	editImportSource(t, source, "INSERT INTO secrets (name, value, set_at, set_by) VALUES ('grafana_viewer_token', 'glsa-imported-token', 1762000800, 'fixture')")
	editImportSource(t, source, "INSERT INTO secrets (name, value, set_at, set_by) VALUES ('notion_token', 'ntn-imported-token', 1762000800, 'fixture')")
	editImportSource(t, source, fmt.Sprintf(`INSERT INTO settings (key, value) VALUES
		('media_tmdb_url', %[1]q),
		('media_tmdb_images_url', %[2]q),
		('media_eztv_url', %[3]q)`, tmdbFake.URL+"/3", tmdbImages.URL, eztvFake.URL))
	editImportSource(t, source, "INSERT INTO secrets (name, value, set_at, set_by) VALUES ('media_tmdb_token', 'tmdb-imported-token', 1762000800, 'fixture')")

	got := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source)
	if got.code != cli.ExitOK {
		t.Fatalf("db import-v2: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	for _, want := range []string{
		"connection service/anthropic: credential api_key carried",
		"connection service/grafana: created from host settings",
		"connection service/grafana: credential viewer_token carried",
		"connection service/notion: created from host settings",
		"connection service/notion: credential token carried",
		"connection service/tmdb: created from host settings",
		"connection service/tmdb: credential read_token carried",
		"connection service/eztv: created from host settings",
		"connection service/weather: created from host settings",
		"connection service/webfetch: created from host settings",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the report lacks %q:\n%s", want, got.stdout)
		}
	}
	if strings.Contains(got.stdout+got.stderr, "sk-ant-fixture") || strings.Contains(got.stdout+got.stderr, "glsa-imported-token") || strings.Contains(got.stdout+got.stderr, "ntn-imported-token") || strings.Contains(got.stdout+got.stderr, "tmdb-imported-token") {
		t.Error("the import printed a credential")
	}

	dest := filepath.Join(dir, "atrium.db")
	q, err := db.OpenSource(dest, importKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()
	if n := importCount(t, q, "SELECT COUNT(*) FROM secrets WHERE name IN ('anthropic_api_key', 'grafana_viewer_token', 'notion_token', 'media_tmdb_token')"); n != 0 {
		t.Errorf("%d migrated credentials stayed in the secrets table", n)
	}
	for _, tc := range []struct{ id, slot, want string }{
		{"service/anthropic", "api_key", "sk-ant-fixture"},
		{"service/grafana", "viewer_token", "glsa-imported-token"},
		{"service/notion", "token", "ntn-imported-token"},
		{"service/tmdb", "read_token", "tmdb-imported-token"},
	} {
		if got := importValue(t, q, "SELECT value FROM connection_credentials WHERE connection_id = ? AND slot = ?", tc.id, tc.slot); got != tc.want {
			t.Errorf("%s credential %s = %q", tc.id, tc.slot, got)
		}
		if got := importValue(t, q, "SELECT set_by FROM connection_credentials WHERE connection_id = ? AND slot = ?", tc.id, tc.slot); got != "fixture" {
			t.Errorf("%s credential %s set_by = %q", tc.id, tc.slot, got)
		}
	}
	if got := importValue(t, q, "SELECT config_json FROM connections WHERE id = 'service/notion'"); !strings.Contains(got, `"api":"https://api.notion.com"`) {
		t.Errorf("the Notion config was not carried: %s", got)
	}
	if got := importValue(t, q, "SELECT config_json FROM connections WHERE id = 'service/grafana'"); !strings.Contains(got, grafana.URL) || !strings.Contains(got, `"metrics_uid":"prom"`) {
		t.Errorf("the Grafana config was not carried: %s", got)
	}
	if got := importValue(t, q, "SELECT config_json FROM connections WHERE id = 'service/weather'"); !strings.Contains(got, "http://weather.invalid/forecast") || !strings.Contains(got, "http://weather.invalid/search") {
		t.Errorf("the weather config was not carried: %s", got)
	}
	if got := importValue(t, q, "SELECT config_json FROM connections WHERE id = 'service/webfetch'"); !strings.Contains(got, "https://example.com/") {
		t.Errorf("the web fetch probe was not carried: %s", got)
	}
	if got := importValue(t, q, "SELECT config_json FROM connections WHERE id = 'service/tmdb'"); !strings.Contains(got, tmdbFake.URL+"/3") || !strings.Contains(got, tmdbImages.URL) {
		t.Errorf("the TMDB config was not carried: %s", got)
	}
	if got := importValue(t, q, "SELECT config_json FROM connections WHERE id = 'service/eztv'"); !strings.Contains(got, eztvFake.URL) {
		t.Errorf("the EZTV config was not carried: %s", got)
	}

	setEnv(t, anthropic.BaseURLEnv, api.URL)
	setEnv(t, settingEnv(grafanatools.KeyURL), grafana.URL)
	ran := runImportApp(t, importTestAppWithHost(t, dir, clock, api.URL, grafana.URL, notionFake.URL), "agent", "run", "--text", "list the datasources")
	if ran.code != cli.ExitOK {
		t.Fatalf("agent run: exit %d stdout %q stderr %q", ran.code, ran.stdout, ran.stderr)
	}
	mu.Lock()
	keys := append([]string(nil), apiKeys...)
	seen := append([]string(nil), authorizations...)
	mu.Unlock()
	if len(keys) == 0 {
		t.Fatalf("the agent run never reached the Anthropic fake: exit %d stdout %q stderr %q", ran.code, ran.stdout, ran.stderr)
	}
	for _, key := range keys {
		if key != "sk-ant-fixture" {
			t.Errorf("Anthropic request used key %q", key)
		}
	}
	if len(seen) == 0 {
		t.Fatal("the Grafana tool never reached the fake")
	}
	for _, authorization := range seen {
		if authorization != "Bearer glsa-imported-token" {
			t.Errorf("Grafana request used %q", authorization)
		}
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM thread_messages WHERE run_id = (SELECT MAX(id) FROM runs) AND content_json LIKE '%Imported datasource%'"); n == 0 {
		t.Error("the Grafana tool result did not reach the thread")
	}

	synced := runImportApp(t, importTestAppWithHost(t, dir, clock, api.URL, grafana.URL, notionFake.URL), "notion", "sync")
	if synced.code != cli.ExitOK {
		t.Fatalf("notion sync: exit %d stdout %q stderr %q", synced.code, synced.stdout, synced.stderr)
	}
	mu.Lock()
	tokens := append([]string(nil), notionTokens...)
	mu.Unlock()
	if len(tokens) == 0 {
		t.Fatal("notion sync never reached the Notion fake")
	}
	for _, token := range tokens {
		if token != "Bearer ntn-imported-token" {
			t.Errorf("Notion request used %q", token)
		}
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM notion_pages WHERE id = ? AND in_trash = 0", notionPage); n != 1 {
		t.Errorf("notion sync stored %d pages for the imported connection", n)
	}

	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	t.Cleanup(func() { db.SetDefaultKey(db.Key{}) })
	host := fmt.Sprintf(`[connections."service/tmdb"]
kind = "service/tmdb"
label = "TMDB"
config = { url = %q, images_url = %q }
capabilities = ["api", "images"]

[connections."service/eztv"]
kind = "service/eztv"
label = "EZTV"
config = { url = %q }
capabilities = ["indexer"]
`, tmdbFake.URL+"/3", tmdbImages.URL, eztvFake.URL)
	hostPath := filepath.Join(dir, "media-host.toml")
	if err := os.WriteFile(hostPath, []byte(host), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", hostPath)
	base := startServeIn(t, dir)
	code, body, _ := fetch(t, &http.Client{}, http.MethodGet, base+"/apps/media/channels?q=Lanterns", nil)
	if code != http.StatusOK || !strings.Contains(body, "Lanterns") {
		t.Fatalf("channels search after import: %d %.300s", code, body)
	}
	tmdbMu.Lock()
	imported := append([]string(nil), tmdbTokens...)
	tmdbMu.Unlock()
	if len(imported) == 0 {
		t.Fatal("the channels page never reached the TMDB fake")
	}
	for _, token := range imported {
		if token != "Bearer tmdb-imported-token" {
			t.Errorf("TMDB request used %q", token)
		}
	}
}

func TestImportV2CarriesTheWebPushConnectionAndUsesItsVAPIDKey(t *testing.T) {
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	scalar := base64.RawURLEncoding.EncodeToString(key.Bytes())
	public := base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	editImportSource(t, source, "INSERT INTO settings (key, value) VALUES ('webpush_subject', 'mailto:ops@example.test')")
	editImportSource(t, source, fmt.Sprintf("INSERT INTO secrets (name, value, set_at, set_by) VALUES ('webpush_vapid_private_key', %q, 1762000800, 'fixture')", scalar))

	got := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source)
	if got.code != cli.ExitOK {
		t.Fatalf("db import-v2: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	for _, want := range []string{
		"connection service/webpush: created from host settings",
		"connection service/webpush: credential vapid_private carried",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the report lacks %q:\n%s", want, got.stdout)
		}
	}
	if strings.Contains(got.stdout+got.stderr, scalar) {
		t.Error("the import printed the VAPID private key")
	}

	dest := filepath.Join(dir, "atrium.db")
	q, err := db.Connect(dest, db.WithKey(importKey(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()
	if stored := importValue(t, q.Read, "SELECT value FROM connection_credentials WHERE connection_id = 'service/webpush' AND slot = 'vapid_private'"); stored != scalar {
		t.Errorf("the imported VAPID key is %q", stored)
	}

	push := newFakePush(t)
	ca := filepath.Join(dir, "push-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: push.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	allowLoopbackPush.Do(func() { notify.UsePushAddresses(func(a netip.Addr) bool { return a.IsLoopback() }) })
	browser := pushBrowser{key: key, secret: make([]byte, 16), endpoint: push.URL + "/push/device-1"}
	_, _ = rand.Read(browser.secret)
	browser.store(t, q)

	tested := runImportApp(t, importTestAppHost(t, dir, clock, webPushOnlyHostConfig(t, dir, ca, "")), "connections", "test", "service/webpush", "--json")
	if tested.code != cli.ExitOK || !strings.Contains(tested.stdout, "delivered to 1 device(s)") {
		t.Fatalf("connections test service/webpush: exit %d stdout %q stderr %q", tested.code, tested.stdout, tested.stderr)
	}
	sent := push.requestFor(t, "/push/device-1")
	checkVAPID(t, sent.header, public, push.URL)
	if payload := browser.decrypt(t, sent.body); payload["title"] != "atrium test" {
		t.Errorf("diagnostic payload %v", payload)
	}
}

func editImportSource(t *testing.T, path, statement string) {
	t.Helper()
	d, err := db.Connect(path, db.WithKey(db.Key{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec(statement); err != nil {
		t.Fatal(err)
	}
}

func TestImportV2MapsAutoActionsToEffects(t *testing.T) {
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	d, err := db.Connect(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id     int64
		msg    int64
		kind   string
		dest   any
		state  string
		error  any
		undone any
		start  any
	}{
		{201, 150, "file", "Archive", "held", "connection lost", nil, nil},
		{202, 151, "correct", "Receipts", "partial", "copied but not expunged", nil, nil},
		{203, 152, "file", "Travel", "undo_held", nil, "write in flight", clock - 200},
		{204, 153, "file", "Work", "undo_settled", nil, "checked", clock - 300},
		{205, 154, "flag", nil, "failed", "refused", nil, nil},
		{206, 155, "trash", "Deleted Messages", "skipped", "protected mailbox", nil, nil},
	} {
		if _, err := d.Write.Exec(
			"INSERT INTO auto_actions (id, ts, account, message_id, digest, kind, dest, undo, state, error, undone_error, undo_started_at) "+
				"VALUES (?, ?, 'icloud', ?, ?, ?, ?, '{\"op\":\"move_back\"}', ?, ?, ?, ?)",
			row.id, clock, row.msg, "digest", row.kind, row.dest, row.state, row.error, row.undone, row.start); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(dir, "atrium.db")
	got := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source)
	if got.code != cli.ExitOK {
		t.Fatalf("db import-v2: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout, "auto_actions: converted 16") {
		t.Errorf("the report lacks the auto conversion count:\n%s", got.stdout)
	}

	q, err := db.OpenSource(dest, importKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()

	effectOf := func(id int64) int64 {
		t.Helper()
		var effect int64
		if err := q.QueryRow("SELECT effect_id FROM effect_import_refs WHERE source_table = 'auto_actions' AND source_id = ? AND role = 'forward'", id).Scan(&effect); err != nil {
			t.Fatalf("auto_actions %d has no effect: %v", id, err)
		}
		return effect
	}
	for _, tc := range []struct {
		id    int64
		state string
	}{
		{201, "unknown"},
		{202, "partial"},
		{203, "done"},
		{204, "done"},
		{205, "failed"},
		{206, "skipped"},
	} {
		if got := importValue(t, q, "SELECT state FROM effects WHERE id = ?", effectOf(tc.id)); got != tc.state {
			t.Errorf("auto_actions %d state %q, want %q", tc.id, got, tc.state)
		}
	}
	for _, id := range []int64{203, 204} {
		if n := importCount(t, q, "SELECT COUNT(*) FROM effects WHERE undo_of = ? AND state = 'unknown'", effectOf(id)); n != 1 {
			t.Errorf("auto_actions %d synthetic undo %d, want one unknown undo", id, n)
		}
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM effect_resolutions WHERE effect_id = (SELECT id FROM effects WHERE undo_of = ?) AND effective_outcome = 'failed'", effectOf(204)); n != 1 {
		t.Errorf("auto_actions 204 resolution %d, want one historical not-undone", n)
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM effect_claims WHERE resource = ?", ledger.AutoKey(150, "file", "Archive")); n != 1 {
		t.Errorf("auto_actions 201 claim %d, want one", n)
	}
}

func TestImportV2MapsAgentWritesToEffects(t *testing.T) {
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	d, err := db.Connect(source, db.WithKey(db.Key{}))
	if err != nil {
		t.Fatal(err)
	}
	undone := clock - 100
	resolved := clock - 50
	for _, row := range []struct {
		id       int64
		state    string
		undone   *float64
		resolved *float64
	}{
		{101, "ok", nil, nil},
		{102, "held", nil, nil},
		{103, "unknown", nil, nil},
		{104, "resolved", nil, &resolved},
		{105, "ok", &undone, nil},
	} {
		if _, err := d.Write.Exec(
			"INSERT INTO agent_writes (id, ts, tool, target, state, undone_at, resolved_at, error) VALUES (?, ?, 'task_add', ?, ?, ?, ?, ?)",
			row.id, clock, "task:"+strconv.FormatInt(row.id, 10), row.state, row.undone, row.resolved, "checked"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Write.Exec(
		"INSERT INTO write_resolutions (ts, actor, source, kind, row_id, proposal_id, outcome, prior_error) VALUES (?, 'operator', 'cli', 'agent_write', 102, NULL, 'resolved', 'checked')",
		clock-10); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(dir, "atrium.db")
	got := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source)
	if got.code != cli.ExitOK {
		t.Fatalf("db import-v2: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout, "agent_writes: converted 12") {
		t.Errorf("the report lacks the agent conversion count:\n%s", got.stdout)
	}
	for _, want := range []string{"agent_writes: synthetic undos 2", "agent_writes: synthetic resolutions 1"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the report lacks %q:\n%s", want, got.stdout)
		}
	}

	q, err := db.OpenSource(dest, importKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()

	effectOf := func(id int64) int64 {
		t.Helper()
		var effect int64
		if err := q.QueryRow("SELECT effect_id FROM effect_import_refs WHERE source_table = 'agent_writes' AND source_id = ? AND role = 'forward'", id).Scan(&effect); err != nil {
			t.Fatalf("agent_writes %d has no effect: %v", id, err)
		}
		return effect
	}
	for _, tc := range []struct {
		id    int64
		state string
	}{
		{101, "done"},
		{102, "unknown"},
		{103, "unknown"},
		{104, "unknown"},
		{105, "done"},
	} {
		if got := importValue(t, q, "SELECT state FROM effects WHERE id = ?", effectOf(tc.id)); got != tc.state {
			t.Errorf("agent_writes %d state %q, want %q", tc.id, got, tc.state)
		}
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM effect_resolutions WHERE effect_id = ? AND outcome = 'resolved' AND effective_outcome = 'partial'", effectOf(102)); n != 1 {
		t.Errorf("agent_writes 102 resolutions %d, want one historical partial", n)
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM effect_resolutions WHERE effect_id = ? AND effective_outcome = 'partial'", effectOf(104)); n != 1 {
		t.Errorf("agent_writes 104 resolutions %d, want one synthetic partial", n)
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM effects WHERE undo_of = ? AND state = 'done'", effectOf(105)); n != 1 {
		t.Errorf("agent_writes 105 synthetic undo %d, want one done undo", n)
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM effects WHERE id = ? AND undone_by IS NOT NULL", effectOf(105)); n != 1 {
		t.Errorf("agent_writes 105 undone_by %d, want set", n)
	}
}

func TestImportV2MapsProposalStatesToEffects(t *testing.T) {
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	d, err := db.Connect(source)
	if err != nil {
		t.Fatal(err)
	}
	proposal := func(id int64, status string, decided, applied, undone any) {
		t.Helper()
		if _, err := d.Write.Exec(
			"INSERT INTO proposals (id, kind, account, status, created_ts, decided_ts, applied_ts, undone_ts, recovered, title, payload, preview, result, fingerprint) "+
				"VALUES (?, 'inbox_backlog', NULL, ?, ?, ?, ?, ?, 0, ?, '{}', '{}', NULL, ?)",
			id, status, clock-1000, decided, applied, undone, fmt.Sprintf("proposal %d", id), fmt.Sprintf("fp-%d", id)); err != nil {
			t.Fatal(err)
		}
	}
	write := func(id, proposalID int64, state, errText string) {
		t.Helper()
		if _, err := d.Write.Exec(
			"INSERT INTO proposal_actions (id, proposal_id, ts, lease, kind, undo_of, action, undo, state, error) "+
				"VALUES (?, ?, ?, ?, 'write', NULL, '{\"op\":\"move\"}', '{\"op\":\"move_back\"}', ?, ?)",
			id, proposalID, clock-1000, clock-1000, state, errText); err != nil {
			t.Fatal(err)
		}
	}
	undo := func(id, proposalID, undoOf int64, state, errText string) {
		t.Helper()
		if _, err := d.Write.Exec(
			"INSERT INTO proposal_actions (id, proposal_id, ts, lease, kind, undo_of, action, undo, state, error) "+
				"VALUES (?, ?, ?, ?, 'undo', ?, '{\"op\":\"move_back\"}', NULL, ?, ?)",
			id, proposalID, clock-1000, clock-1000, undoOf, state, errText); err != nil {
			t.Fatal(err)
		}
	}
	proposal(21, "pending", nil, nil, nil)
	proposal(22, "applied", clock-900, clock-800, nil)
	write(221, 22, "ok", "")
	proposal(23, "applied", clock-900, clock-800, nil)
	write(231, 23, "ok", "")
	write(232, 23, "failed", "refused")
	proposal(24, "failed", clock-900, nil, nil)
	write(241, 24, "failed", "refused")
	proposal(25, "applied", clock-900, clock-800, clock-500)
	write(251, 25, "ok", "")
	undo(252, 25, 251, "ok", "")
	proposal(26, "applied", clock-900, clock-800, nil)
	write(261, 26, "ok", "")
	undo(262, 26, 261, "failed", "refused")
	proposal(27, "failed", clock-900, nil, nil)
	write(271, 27, "held", "connection lost")
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(dir, "atrium.db")
	got := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source)
	if got.code != cli.ExitOK {
		t.Fatalf("db import-v2: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout, "proposal_actions: converted 15") {
		t.Errorf("the report lacks the proposal conversion count:\n%s", got.stdout)
	}

	q, err := db.OpenSource(dest, importKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()

	effectOf := func(actionID int64, role string) int64 {
		t.Helper()
		var effect int64
		if err := q.QueryRow("SELECT effect_id FROM effect_import_refs WHERE source_table = 'proposal_actions' AND source_id = ? AND role = ?", actionID, role).Scan(&effect); err != nil {
			t.Fatalf("proposal_actions %d (%s) has no effect: %v", actionID, role, err)
		}
		return effect
	}
	for _, tc := range []struct {
		action    int64
		role      string
		direction string
		state     string
	}{
		{221, "forward", "forward", "done"},
		{231, "forward", "forward", "done"},
		{232, "forward", "forward", "failed"},
		{241, "forward", "forward", "failed"},
		{251, "forward", "forward", "done"},
		{252, "undo", "undo", "done"},
		{261, "forward", "forward", "done"},
		{262, "undo", "undo", "failed"},
		{271, "forward", "forward", "unknown"},
	} {
		var direction, state string
		if err := q.QueryRow("SELECT direction, state FROM effects WHERE id = ?", effectOf(tc.action, tc.role)).Scan(&direction, &state); err != nil {
			t.Fatal(err)
		}
		if direction != tc.direction || state != tc.state {
			t.Errorf("proposal_actions %d = %s/%s, want %s/%s", tc.action, direction, state, tc.direction, tc.state)
		}
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM effects WHERE id = ? AND undo_of = ?", effectOf(252, "undo"), effectOf(251, "forward")); n != 1 {
		t.Errorf("the completed undo does not point at its forward row")
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM effects WHERE id = ? AND undone_by = ?", effectOf(251, "forward"), effectOf(252, "undo")); n != 1 {
		t.Errorf("the undone forward row does not point at its completed undo")
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM effects WHERE id = ? AND undone_by IS NULL", effectOf(261, "forward")); n != 1 {
		t.Errorf("the forward row of a failed undo was marked undone")
	}
	if got := importValue(t, q, "SELECT error FROM effects WHERE id = ?", effectOf(271, "forward")); got != "connection lost" {
		t.Errorf("the blocked write error is %q", got)
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM effect_claims WHERE resource = 'proposal/27/move'"); n != 1 {
		t.Errorf("the blocked write claim = %d, want one", n)
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM effect_resources WHERE effect_id = ? AND resource = 'proposal/27/move' AND holding = 1", effectOf(271, "forward")); n != 1 {
		t.Errorf("the blocked write does not hold its resource")
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM effects WHERE proposal_id = 21"); n != 0 {
		t.Errorf("the pending proposal produced %d effects", n)
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM effect_resolutions WHERE effect_id IN (SELECT effect_id FROM effect_import_refs WHERE source_table = 'proposal_actions')"); n != 0 {
		t.Errorf("the import invented %d resolutions for settled proposal work", n)
	}
	for _, tc := range []struct {
		id     int64
		status string
		undone bool
	}{
		{21, "pending", false},
		{22, "applied", false},
		{23, "applied", false},
		{24, "failed", false},
		{25, "applied", true},
		{26, "applied", false},
		{27, "failed", false},
	} {
		if got := importValue(t, q, "SELECT status FROM proposals WHERE id = ?", tc.id); got != tc.status {
			t.Errorf("proposal %d status %q, want %q", tc.id, got, tc.status)
		}
		if undone := !importNull(t, q, "SELECT undone_ts FROM proposals WHERE id = ?", tc.id); undone != tc.undone {
			t.Errorf("proposal %d undone = %v, want %v", tc.id, undone, tc.undone)
		}
	}

	listed := runImportApp(t, importTestApp(t, dir, clock), "proposals", "list")
	if listed.code != cli.ExitOK {
		t.Fatalf("proposals list: exit %d stdout %q stderr %q", listed.code, listed.stdout, listed.stderr)
	}
	var rows []struct {
		ID       int64 `json:"id"`
		Undoable bool  `json:"undoable"`
	}
	if err := json.Unmarshal([]byte(listed.stdout), &rows); err != nil {
		t.Fatalf("proposals list output %q: %v", listed.stdout, err)
	}
	undoable := map[int64]bool{}
	for _, row := range rows {
		undoable[row.ID] = row.Undoable
	}
	for _, tc := range []struct {
		id   int64
		want bool
	}{
		{21, false},
		{22, true},
		{23, true},
		{24, false},
		{25, false},
		{26, true},
		{27, false},
	} {
		if got, ok := undoable[tc.id]; !ok || got != tc.want {
			t.Errorf("proposals list %d undoable = %v (present %v), want %v", tc.id, got, ok, tc.want)
		}
	}

	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	t.Cleanup(func() { db.SetDefaultKey(db.Key{}) })
	base := startServeIn(t, dir)
	status, body, _ := fetch(t, http.DefaultClient, http.MethodGet, base+"/review", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /review = %d", status)
	}
	if !strings.Contains(body, "/proposals/27/resolve") || !strings.Contains(body, "connection lost") {
		t.Errorf("the review page does not offer to resolve the blocked proposal with its error: %.400s", body)
	}
	for _, id := range []int64{21, 22, 23, 24, 25, 26} {
		if strings.Contains(body, fmt.Sprintf("/proposals/%d/resolve", id)) {
			t.Errorf("the review page treats proposal %d as blocked", id)
		}
	}
}

func TestImportV2BillsMemoryAndRefusesOverBudgetAgentCalls(t *testing.T) {
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	loc, err := time.LoadLocation("America/Port_of_Spain")
	if err != nil {
		t.Fatal(err)
	}
	month := inference.MonthStart(clock, loc)
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	editImportSource(t, source, fmt.Sprintf(`INSERT INTO usage (id, ts, request_id, source, provider, thread_id, run_id, kind, job_name, model, effort, iterations, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, reasoning_tokens, searches, tool_calls, wall_ms, tier, cost_usd, partial, unpriced, clear_edits, cleared_tokens, compaction) VALUES
		(101, %[1]f, 'mem-1', 'loop', 'anthropic', 1, NULL, 'memory', NULL, 'claude-haiku-5-5', 'low', '[]', 10, 0, 0, 0, 5, 0, 0, 0, 10, '<=100K', 0.5, 0, 0, 0, 0, 0),
		(102, %[1]f, 'sub-1', 'claude_subscription', 'anthropic', 1, NULL, 'chat', NULL, 'claude-haiku-5-5', 'low', '[]', 10, 0, 0, 0, 5, 0, 0, 0, 10, '<=100K', 1000, 0, 0, 0, 0, 0),
		(103, %[1]f, 'part-1', 'loop', 'anthropic', 1, NULL, 'chat', NULL, 'claude-haiku-5-5', 'low', '[]', 10, 0, 0, 0, 5, 0, 0, 0, 10, '<=100K', 0.25, 1, 1, 0, 0, 0),
		(104, %[2]f, 'prev-1', 'loop', 'anthropic', 1, NULL, 'chat', NULL, 'claude-haiku-5-5', 'low', '[]', 10, 0, 0, 0, 5, 0, 0, 0, 10, '<=100K', 5, 0, 0, 0, 0, 0),
		(105, %[3]f, 'bound-1', 'loop', 'anthropic', 1, NULL, 'chat', NULL, 'claude-haiku-5-5', 'low', '[]', 10, 0, 0, 0, 5, 0, 0, 0, 10, '<=100K', 0.1, 0, 0, 0, 0, 0)`,
		clock-3600, month-3600, month+3600))
	dest := filepath.Join(dir, "atrium.db")
	if got := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source); got.code != cli.ExitOK {
		t.Fatalf("db import-v2: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	q, err := db.Connect(dest, db.WithKey(importKey(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()

	if n := importCount(t, q.Read, "SELECT COUNT(*) FROM usage"); n != 26 {
		t.Errorf("usage rows %d, want 26", n)
	}
	if n := importCount(t, q.Read, "SELECT COUNT(*) FROM usage WHERE state = 'settled'"); n != 26 {
		t.Errorf("%d rows are not settled", 26-n)
	}
	if n := importCount(t, q.Read, "SELECT COUNT(*) FROM usage WHERE run_id IS NOT NULL"); n != 0 {
		t.Errorf("%d usage rows kept a run link", n)
	}
	if got := importValue(t, q.Read, "SELECT budget_class FROM usage WHERE id = 101"); got != "unattended" {
		t.Errorf("memory budget class %q", got)
	}
	if got := importValue(t, q.Read, "SELECT budget_class FROM usage WHERE id = 102"); got != "subscription" {
		t.Errorf("subscription budget class %q", got)
	}
	if got := importValue(t, q.Read, "SELECT budget_class FROM usage WHERE id = 103"); got != "interactive" {
		t.Errorf("partial budget class %q", got)
	}
	if n := importCount(t, q.Read, "SELECT accounted_nusd FROM usage WHERE id = 101"); n != 500000000 {
		t.Errorf("memory accounted nusd %d", n)
	}
	if n := importCount(t, q.Read, "SELECT partial FROM usage WHERE id = 103"); n != 1 {
		t.Errorf("partial flag %d", n)
	}
	if n := importCount(t, q.Read, "SELECT unpriced FROM usage WHERE id = 103"); n != 1 {
		t.Errorf("unpriced flag %d", n)
	}
	if n := importCount(t, q.Read, "SELECT COUNT(*) FROM usage WHERE request_id = 'prev-1'"); n != 0 {
		t.Error("the previous month's usage row was carried")
	}
	status, err := inference.StatusAt(settings.With(context.Background(), settings.New()), q.Read, clock, loc)
	if err != nil {
		t.Fatal(err)
	}
	var expected float64
	if err := q.Read.QueryRowContext(context.Background(), "SELECT COALESCE(SUM(cost_usd), 0) FROM usage WHERE budget_class <> 'subscription'").Scan(&expected); err != nil {
		t.Fatal(err)
	}
	if status.CommittedUSD < expected-0.001 || status.CommittedUSD > expected+0.001 {
		t.Errorf("committed %.6f, want about %.6f", status.CommittedUSD, expected)
	}
	if status.CommittedUSD >= 1000 {
		t.Error("subscription spend entered the monthly budget")
	}
	if _, err := q.Write.ExecContext(context.Background(), "INSERT OR REPLACE INTO settings (key, value) VALUES ('monthly_budget_usd', '1')"); err != nil {
		t.Fatal(err)
	}
	api := anthropictest.New(t, anthropictest.Reply("end_turn", anthropictest.Text("should not be sent")))
	setEnv(t, anthropic.BaseURLEnv, api.URL)
	ran := runImportApp(t, importTestApp(t, dir, clock), "agent", "run", "--text", "hello")
	if ran.code == cli.ExitOK || !strings.Contains(ran.stdout+ran.stderr, "monthly budget") {
		t.Fatalf("over-budget agent run: exit %d stdout %q stderr %q", ran.code, ran.stdout, ran.stderr)
	}
	if n := api.Remaining(); n != 1 {
		t.Errorf("%d fake responses were consumed", 1-n)
	}
}

func TestImportV2UndoesImportedAutomaticActions(t *testing.T) {
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	d, err := db.Connect(source)
	if err != nil {
		t.Fatal(err)
	}
	moveBack := func(uid uint32) string {
		return fmt.Sprintf(`{"op":"move_back","account":"icloud","src_imap":"INBOX","dest_imap":"Bills",`+
			`"dest_uidvalidity":3,"items":[{"id":0,"msgid":"","uid":%d,"dest_uid":%d}]}`, uid, uid)
	}
	for _, row := range []struct {
		id     int64
		msg    int64
		state  string
		uid    uint32
		undone any
	}{
		{301, 160, "applied", 1, nil},
		{302, 161, "applied", 2, nil},
		{303, 162, "undo_settled", 3, "checked"},
	} {
		if _, err := d.Write.Exec(
			"INSERT INTO auto_actions (id, ts, account, message_id, digest, kind, dest, undo, state, undone_error) "+
				"VALUES (?, ?, 'icloud', ?, 'digest', 'file', 'Bills', ?, ?, ?)",
			row.id, clock, row.msg, moveBack(row.uid), row.state, row.undone); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	fake := mailtest.NewFakeClient()
	fake.AddBox("INBOX", mailtest.NewBox(1))
	fake.AddBox("Bills", mailtest.NewBox(3))
	for _, uid := range []uint32{1, 2, 3} {
		h, x := mailtest.RawWith("Filed statement", "statements@bank.test", fmt.Sprintf("filed-%d", uid), "Filed.")
		if got := fake.Boxes["Bills"].Box.Add(h, x, nil, nil); got != uid {
			t.Fatalf("Bills uid %d, want %d", got, uid)
		}
	}

	vars := varsFor(t).snapshot()
	vars["ATRIUM_DB"] = filepath.Join(dir, "atrium.db")
	vars["ATRIUM_CACHE_DIR"] = filepath.Join(dir, "cache")
	vars["ATRIUM_DB_KEY"] = rightKeyHex
	vars[settingEnv(config.KeyMailAccountsFile)] = defaultAccountsFile
	delete(vars, "ATRIUM_DB_KEY_FILE")
	reg := settings.New()
	t.Cleanup(func() { db.SetDefaultKey(db.Key{}) })
	app := newAppWith(config.Map(vars), reg, config.NewState(reg), nil,
		func(config.Config) mail.OpenFunc { return fake.Open }, nil, quietLog)

	dest := filepath.Join(dir, "atrium.db")
	if got := runImportApp(t, app, "db", "import-v2", source); got.code != cli.ExitOK {
		t.Fatalf("db import-v2: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}

	q, err := db.OpenSource(dest, importKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()
	effectOf := func(id int64) int64 {
		t.Helper()
		var effect int64
		if err := q.QueryRow("SELECT effect_id FROM effect_import_refs WHERE source_table = 'auto_actions' AND source_id = ? AND role = 'forward'", id).Scan(&effect); err != nil {
			t.Fatalf("auto_actions %d has no effect: %v", id, err)
		}
		return effect
	}

	for _, id := range []int64{301, 302} {
		effect := effectOf(id)
		if n := importCount(t, q, "SELECT COUNT(*) FROM effect_resources WHERE effect_id = ? AND holding = 1", effect); n != 1 {
			t.Errorf("auto_actions %d holds %d resources, want only the automatic key", id, n)
		}
		if n := importCount(t, q, "SELECT COUNT(*) FROM effect_resources WHERE effect_id = ? AND resource = ?", effect,
			ledger.AutoKey(160+id-301, "file", "Bills")); n != 1 {
			t.Errorf("auto_actions %d does not hold its automatic key", id)
		}
	}
	settled := effectOf(303)
	if n := importCount(t, q, "SELECT COUNT(*) FROM effect_resources WHERE effect_id = ? AND resource = ? AND holding = 1",
		settled, ledger.AutoKey(162, "file", "Bills")); n != 1 {
		t.Errorf("the undo_settled forward lost its automatic-success key")
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM effect_resources WHERE effect_id = ? AND holding = 1", settled); n != 1 {
		t.Errorf("auto_actions 303 holds %d resources, want only the automatic key", n)
	}

	for _, id := range []int64{301, 302} {
		effect := effectOf(id)
		if got := runImportApp(t, app, "live", "undo", strconv.FormatInt(effect, 10)); got.code != cli.ExitOK {
			t.Fatalf("live undo of imported auto_actions %d: exit %d stdout %q stderr %q", id, got.code, got.stdout, got.stderr)
		}
		if undone := importValue(t, q, "SELECT CAST(undone_by IS NOT NULL AS TEXT) FROM effects WHERE id = ?", effect); undone != "1" {
			t.Errorf("imported auto_actions %d after undo is %s, want undone", id, undone)
		}
	}
	if n := len(fake.Boxes["INBOX"].Box.Messages); n != 2 {
		t.Fatalf("INBOX holds %d messages after undoing imported history, want 2", n)
	}
	if n := len(fake.Boxes["Bills"].Box.Messages); n != 1 {
		t.Fatalf("Bills holds %d messages after undoing imported history, want the untouched one", n)
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM effects WHERE direction = 'undo' AND state = 'done' AND undo_of IN (?, ?)",
		effectOf(301), effectOf(302)); n != 2 {
		t.Errorf("%d completed undos of the imported actions, want 2", n)
	}
}

func TestImportV2ResolvingAnImportedUndoClearsTheCorrection(t *testing.T) {
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	d, err := db.Connect(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (100, 'icloud', 'Bills', ?, 'operator')", clock); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec(
		"INSERT INTO auto_actions (id, ts, account, message_id, digest, kind, dest, undo, state) "+
			"VALUES (311, ?, 'icloud', 100, 'digest', 'correct', 'Bills', '{\"op\":\"move_back\"}', 'undo_unknown')", clock); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	app := importTestApp(t, dir, clock)
	if got := runImportApp(t, app, "db", "import-v2", source); got.code != cli.ExitOK {
		t.Fatalf("db import-v2: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	dest := filepath.Join(dir, "atrium.db")
	q, err := db.OpenSource(dest, importKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()
	var forward int64
	if err := q.QueryRow("SELECT effect_id FROM effect_import_refs WHERE source_table = 'auto_actions' AND source_id = 311 AND role = 'forward'").Scan(&forward); err != nil {
		t.Fatalf("auto_actions 311 has no effect: %v", err)
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM corrections WHERE message_id = 100"); n != 1 {
		t.Fatalf("%d corrections after import, want the carried one", n)
	}
	if got := runImportApp(t, app, "live", "resolve", strconv.FormatInt(forward, 10), "--outcome", "undone"); got.code != cli.ExitOK {
		t.Fatalf("live resolve: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if n := importCount(t, q, "SELECT COUNT(*) FROM corrections WHERE message_id = 100"); n != 0 {
		t.Fatalf("%d corrections after resolving the undo as undone, want the correction cleared", n)
	}
	if undone := importValue(t, q, "SELECT CAST(undone_by IS NOT NULL AS TEXT) FROM effects WHERE id = ?", forward); undone != "1" {
		t.Fatalf("the forward after the resolution is %s, want undone", undone)
	}
}

func TestImportV2MapsUnresolvedAgentClaimsToWriterKeys(t *testing.T) {
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	d, err := db.Connect(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('media_qbittorrent_url', 'http://qb.local:8080/')"); err != nil {
		t.Fatal(err)
	}
	const hash = "ABCDEF0123456789ABCDEF0123456789ABCDEF01"
	for _, row := range []struct {
		id     int64
		tool   string
		target string
		state  string
	}{
		{201, "track_package", "package:1", "held"},
		{202, "media_add_magnet", hash, "unknown"},
		{203, "mail_move", "icloud:INBOX->Bills", "held"},
		{204, "mail_folder_create", "icloud:Archive", "held"},
		{205, "calendar_delete", "icloud:event-1", "unknown"},
		{206, "task_add", "task:9", "unknown"},
	} {
		if _, err := d.Write.Exec(
			"INSERT INTO agent_writes (id, ts, tool, target, state) VALUES (?, ?, ?, ?, ?)",
			row.id, clock, row.tool, row.target, row.state); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "atrium.db")
	got := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source)
	if got.code != cli.ExitOK {
		t.Fatalf("db import-v2: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout, "agent_writes: conservative claim legacy_agent/task:9") {
		t.Errorf("the report does not name the fallback claim:\n%s", got.stdout)
	}
	q, err := db.OpenSource(dest, importKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()
	for _, tc := range []struct {
		id  int64
		key string
	}{
		{201, "tracking:ups:1Z999AA10123456784"},
		{202, "qbit:http://qb.local:8080:" + strings.ToLower(hash)},
		{203, "mail_move:icloud:INBOX->Bills"},
		{204, "mail_folder:icloud:Archive"},
		{205, "calendar:icloud:event-1"},
		{206, "legacy_agent/task:9"},
	} {
		var effect int64
		if err := q.QueryRow("SELECT effect_id FROM effect_import_refs WHERE source_table = 'agent_writes' AND source_id = ? AND role = 'forward'", tc.id).Scan(&effect); err != nil {
			t.Fatalf("agent_writes %d has no effect: %v", tc.id, err)
		}
		var key string
		if err := q.QueryRow("SELECT resource FROM effect_resources WHERE effect_id = ? AND holding = 1", effect).Scan(&key); err != nil {
			t.Fatalf("agent_writes %d has no claim: %v", tc.id, err)
		}
		if key != tc.key {
			t.Errorf("agent_writes %d claim = %q, want %q", tc.id, key, tc.key)
		}
	}
}

func TestImportV2RefusesAnUnknownSettingWithItsName(t *testing.T) {
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	d, err := db.Connect(source, db.WithKey(db.Key{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO settings (key, value) VALUES ('not_a_real_key', 'x')"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	got := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source)
	if got.code == cli.ExitOK || !strings.Contains(got.stderr, "not_a_real_key") {
		t.Fatalf("an unknown setting: exit %d stderr %q, want a refusal naming it", got.code, got.stderr)
	}
}

func TestImportV2ResetsLiveMailToShadowAndAccountsEverySetting(t *testing.T) {
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	d, err := db.Connect(source, db.WithKey(db.Key{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO settings (key, value) VALUES ('mail_mode', 'live')"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	got := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source)
	if got.code != cli.ExitOK {
		t.Fatalf("db import-v2: exit %d stderr %q", got.code, got.stderr)
	}
	for _, want := range []string{
		"setting mail_mode: reset-for-cutover live -> shadow",
		"setting cache_ttl_chat: carried",
		"setting default_context_limit: carried",
		"setting default_effort: carried",
		"setting money_since: dropped",
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
	if value := importValue(t, dest, "SELECT value FROM settings WHERE key = 'mail_mode'"); value != "shadow" {
		t.Fatalf("imported mail_mode = %q, want shadow", value)
	}
}
