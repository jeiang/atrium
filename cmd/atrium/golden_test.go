package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/calendar"
	caldav "github.com/jeiang/atrium/internal/calendar/caldavtest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/mail/rebalance"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

type cliGolden struct {
	Name     string            `json:"name"`
	Argv     []string          `json:"argv"`
	ExitCode int               `json:"exit_code"`
	Stdout   string            `json:"stdout"`
	Stderr   string            `json:"stderr"`
	Env      map[string]string `json:"env"`
	Stdin    string            `json:"stdin"`
	Files    map[string]string `json:"files"`
}

type worldMessage struct {
	UID    uint32   `json:"uid"`
	Flags  []string `json:"flags"`
	Labels []string `json:"labels"`
	Header string   `json:"header"`
	Text   string   `json:"text"`
	ModSeq uint64   `json:"modseq"`
}

type worldAccount struct {
	Gmail      bool                      `json:"gmail"`
	Mailboxes  map[string][]worldMessage `json:"mailboxes"`
	ModSeq     map[string]uint64         `json:"modseq"`
	NextUID    map[string]uint32         `json:"next_uid"`
	Order      []string                  `json:"order"`
	SpecialUse map[string][]string       `json:"special_use"`
	Validity   uint32                    `json:"uidvalidity"`
}

type goldenWorld struct {
	Clock      float64                 `json:"clock"`
	Accounts   map[string]worldAccount `json:"accounts"`
	Categories map[string][]uint32     `json:"gmail_categories"`
}

func loadWorld(t *testing.T) goldenWorld {
	t.Helper()
	var w goldenWorld
	if err := json.Unmarshal(golden.ReadFile(t, "cli", "world.json"), &w); err != nil {
		t.Fatal(err)
	}
	return w
}

// fakes rebuilds the IMAP servers the frozen app talked to when it recorded the goldens.
func (w goldenWorld) fakes(env config.Env) map[string]*mailtest.FakeClient {
	out := map[string]*mailtest.FakeClient{}
	for name, spec := range w.Accounts {
		fake := mailtest.NewFakeClient(env)
		fake.Gmail = spec.Gmail
		for _, box := range spec.Order {
			fb := &mailtest.FakeBox{Validity: spec.Validity, ModSeq: spec.ModSeq[box], NextUID: spec.NextUID[box], Messages: map[uint32]*mailtest.FakeMessage{}}
			for _, m := range spec.Mailboxes[box] {
				fb.Messages[m.UID] = &mailtest.FakeMessage{Header: []byte(m.Header), Text: []byte(m.Text), Flags: m.Flags, Labels: m.Labels, ModSeq: m.ModSeq}
			}
			fake.AddBox(box, fb, spec.SpecialUse[box]...)
		}
		if spec.Gmail {
			fake.CategoryUIDs = w.Categories
		}
		out[name] = fake
	}
	return out
}

type llmScript struct {
	Defaults  map[string]json.RawMessage            `json:"defaults"`
	BySubject map[string]map[string]json.RawMessage `json:"by_subject"`
}

func subjectOf(prompt string) (string, bool) {
	for line := range strings.SplitSeq(prompt, "\n") {
		if subject, ok := strings.CutPrefix(line, "Subject: "); ok {
			return subject, true
		}
	}
	return "", false
}

func sparseEmbedding(text string) []float64 {
	v := make([]float64, config.EmbedDim)
	for _, token := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		sum := sha256.Sum256([]byte(token))
		v[binary.BigEndian.Uint32(sum[:4])%config.EmbedDim]++
	}
	return v
}

// models serves the llama.cpp stand-in of the generator: embeddings are bucketed token counts and a chat reply is
// the script's default for the feature the system prompt names, with the override of the message's subject.
func models(t *testing.T) *httptest.Server {
	t.Helper()
	var script llmScript
	if err := json.Unmarshal(golden.ReadFile(t, "pipeline", "llm_script.json"), &script); err != nil {
		t.Fatal(err)
	}
	features := map[string]string{
		llm.FolderSystem: "folder", llm.FlagSystem: "flag", llm.EventSystem: "event",
		openloops.CommitmentSystem: "commitments", watches.PackageSystem: "package", rebalance.NameSystem: "folder_name",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input    []string `json:"input"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			data := make([]any, len(req.Input))
			for i, text := range req.Input {
				data[i] = map[string]any{"index": i, "embedding": sparseEmbedding(text)}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			return
		}
		feature, known := features[req.Messages[0].Content]
		if !known {
			http.Error(w, "unknown system prompt", http.StatusBadRequest)
			return
		}
		answer := script.Defaults[feature]
		if subject, found := subjectOf(req.Messages[1].Content); found {
			if override, ok := script.BySubject[subject][feature]; ok && feature != "folder_name" {
				merged := map[string]json.RawMessage{}
				_ = json.Unmarshal(answer, &merged)
				over := map[string]json.RawMessage{}
				_ = json.Unmarshal(override, &over)
				for k, v := range over {
					merged[k] = v
				}
				answer, _ = json.Marshal(merged)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(answer)}}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o777)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), data, 0o666)
	})
	if err != nil {
		t.Fatal(err)
	}
}

var pythonOrder = []string{
	"sync", "watch", "rules", "shadow-report", "reindex", "rebalance", "deletions", "gmail-labels", "live", "proposals",
	"notion", "agent", "calendar", "loops", "watches", "money", "media", "serve", "codemode-worker",
}

// withUnported fills the commands other slices still port with name-only stand-ins in cli.py's order, so usage lines
// list every command the Python parser had.
func withUnported(have []*cli.Command) []*cli.Command {
	var out []*cli.Command
	for _, name := range pythonOrder {
		i := slices.IndexFunc(have, func(c *cli.Command) bool { return c.Name == name })
		switch {
		case i >= 0:
			out = append(out, have[i])
		default:
			out = append(out, &cli.Command{Name: name, Setup: noop})
		}
	}
	return out
}

func noop(*cli.Args) cli.Handler { return func(*cli.Env) int { return cli.ExitOK } }

// unported are goldens of commands whose proposal kind or executor another slice ports; each is enabled there.
var unported = map[string]string{}

type parityCase struct {
	Name     string  `json:"name"`
	GoStdout *string `json:"go_stdout"`
	GoStderr *string `json:"go_stderr"`
}

type parityRow struct {
	Command string       `json:"command"`
	Path    []string     `json:"path"`
	Status  string       `json:"status"`
	Cases   []parityCase `json:"cases"`
}

func loadParity(t *testing.T) []parityRow {
	t.Helper()
	var table struct {
		Commands []parityRow `json:"commands"`
	}
	if err := json.Unmarshal(golden.ReadFile(t, "cli", "parity.json"), &table); err != nil {
		t.Fatal(err)
	}
	return table.Commands
}

func portedCommands(rows []parityRow) map[string]bool {
	out := map[string]bool{}
	for _, row := range rows {
		if row.Status == "ported" && len(row.Path) > 0 {
			out[row.Path[0]] = true
		}
	}
	return out
}

var volatileRunFields = []string{"started_at", "ended_at", "lease_until", "owner"}

func normalizeRun(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		for _, field := range volatileRunFields {
			if strings.HasPrefix(strings.TrimLeft(line, " "), `"`+field+`": `) {
				lines[i] = `"` + field + `": <volatile>`
			}
		}
	}
	return strings.Join(lines, "\n")
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

type goldenData struct {
	world       goldenWorld
	environment map[string]string
}

func loadGoldenData(t *testing.T) goldenData {
	t.Helper()
	var scenario struct {
		Environment map[string]string `json:"environment"`
	}
	if err := json.Unmarshal(golden.ReadFile(t, "pipeline", "scenario.json"), &scenario); err != nil {
		t.Fatal(err)
	}
	return goldenData{world: loadWorld(t), environment: scenario.Environment}
}

// rig is the app over a copy of the golden database in a fresh directory, with the generator's IMAP and
// llama.cpp stand-ins behind it and the clock the goldens were recorded at.
type rig struct {
	dir string
	app *cli.App
}

func newRig(t *testing.T, data goldenData, extraEnv map[string]string, stdin string) *rig {
	t.Helper()
	dir := t.TempDir()
	copyGoldenFile(t, filepath.Join(golden.Dir(), "atrium.db"), filepath.Join(dir, "atrium.db"))
	copyTree(t, filepath.Join(golden.Dir(), "state", "cache"), filepath.Join(dir, "cache"))
	srv := models(t)
	t.Cleanup(calendar.UseEndpoints(calendar.Endpoints{CalDAV: caldav.New(t, data.environment["ICLOUD_APPLE_ID"], data.environment["ICLOUD_APP_PASSWORD"]).URL + "/"}))
	vars := map[string]string{}
	for k, v := range data.environment {
		vars[k] = v
	}
	vars["ATRIUM_DB"] = filepath.Join(dir, "atrium.db")
	vars["ATRIUM_CACHE_DIR"] = filepath.Join(dir, "cache")
	vars["ATRIUM_CONTACTS_DIR"] = filepath.Join(golden.Dir(), "state", "contacts")
	vars["ATRIUM_MEDIA_ROOT"] = filepath.Join(dir, "media")
	vars["ATRIUM_CHAT_URL"] = srv.URL + "/v1"
	vars["ATRIUM_EMBED_URL"] = srv.URL + "/v1"
	for k, v := range extraEnv {
		vars[k] = v
	}
	for k, v := range vars {
		t.Setenv(k, v)
	}
	fakes := data.world.fakes(config.Map(vars))
	clients := func(config.Config, config.Env) func(context.Context, string) (mail.Client, error) {
		return func(_ context.Context, account string) (mail.Client, error) { return fakes[account], nil }
	}
	app := cli.New("atrium", logx.Setup)
	app.Add(withUnported(commands(clients, func() float64 { return data.world.Clock }))...)
	app.Stdin = strings.NewReader(stdin)
	return &rig{dir: dir, app: app}
}

func (r *rig) run(ctx context.Context, argv ...string) (code int, stdout, stderr string) {
	var out, errs bytes.Buffer
	code = r.app.Run(ctx, argv, &out, &errs)
	return code, out.String(), errs.String()
}

// TestCLIGoldens runs the recorded mail commands on a copy of the golden database and compares exit code and
// stdout, the last stderr line of a failure and, for argument errors and help, the complete stderr.
func TestCLIGoldens(t *testing.T) {
	var index []cliGolden
	if err := json.Unmarshal(golden.ReadFile(t, "cli", "index.json"), &index); err != nil {
		t.Fatal(err)
	}
	data := loadGoldenData(t)
	rows := loadParity(t)
	ported := portedCommands(rows)
	overrides := map[string]parityCase{}
	for _, row := range rows {
		for _, c := range row.Cases {
			overrides[c.Name] = c
		}
	}
	ran := 0
	for _, g := range index {
		parser := strings.HasPrefix(g.Name, "argparse-")
		if !parser && !ported[g.Argv[1]] {
			continue
		}
		ran++
		t.Run(g.Name, func(t *testing.T) {
			if reason, skip := unported[g.Name]; skip {
				t.Skip(reason)
			}
			r := newRig(t, data, goldenEnv(t, g), g.Stdin)
			tmp := filepath.Join(r.dir, "tmp")
			if err := os.MkdirAll(tmp, 0o777); err != nil {
				t.Fatal(err)
			}
			for name, content := range g.Files {
				if err := os.WriteFile(filepath.Join(tmp, name), []byte(content), 0o666); err != nil {
					t.Fatal(err)
				}
			}
			argv := make([]string, len(g.Argv)-1)
			for i, arg := range g.Argv[1:] {
				argv[i] = strings.ReplaceAll(arg, "{tmp}", tmp)
			}
			limit := 60 * time.Second
			if len(argv) > 0 && argv[0] == "watch" {
				limit = 5 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			code, stdout, stderr := r.run(ctx, argv...)
			if code != g.ExitCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, g.ExitCode, stderr)
			}
			want := string(golden.ReadFile(t, "cli", g.Stdout))
			wantErr := string(golden.ReadFile(t, "cli", g.Stderr))
			if o := overrides[g.Name]; o.GoStdout != nil {
				want = *o.GoStdout
			}
			if o := overrides[g.Name]; o.GoStderr != nil {
				wantErr = *o.GoStderr
			}
			if !parser && g.Argv[1] == "agent" {
				stdout, want = normalizeRun(stdout), normalizeRun(want)
			}
			if stdout != want {
				t.Errorf("stdout = %q, want %q", stdout, want)
			}
			switch {
			case parser && (g.ExitCode != 0 || wantErr == "") && stderr != wantErr:
				t.Errorf("stderr = %q, want %q", stderr, wantErr)
			case !parser && g.ExitCode != 0 && lastLine(stderr) != lastLine(wantErr):
				t.Errorf("last stderr line = %q, want %q", lastLine(stderr), lastLine(wantErr))
			}
		})
	}
	if ran == 0 {
		t.Fatal("no goldens ran")
	}
}

// TestWritingCommandsTakeTheDatabaseLock holds atrium.db.lock and expects every command that holds it in
// Python (the rebaseline and deletion passes and the loops and watches refreshes) to wait for it.
func TestWritingCommandsTakeTheDatabaseLock(t *testing.T) {
	data := loadGoldenData(t)
	for _, argv := range [][]string{{"live", "rebaseline"}, {"live", "deletions"}, {"loops", "refresh"}, {"watches", "refresh"}} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			r := newRig(t, data, nil, "")
			lock, err := db.Exclusive(filepath.Join(r.dir, "atrium.db"))
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan int, 1)
			go func() {
				code, _, _ := r.run(context.Background(), argv...)
				done <- code
			}()
			select {
			case code := <-done:
				t.Fatalf("exited %d while the lock was held", code)
			case <-time.After(500 * time.Millisecond):
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case code := <-done:
				if code != cli.ExitOK {
					t.Errorf("exit code = %d after the lock was released", code)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("the command did not finish after the lock was released")
			}
		})
	}
}

func copyGoldenFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o666); err != nil {
		t.Fatal(err)
	}
}

// TestCLIParity checks the generated parity table (parity.json) against the production binary's command set:
// every ported row answers -h, every excluded row is refused as the production parser refuses an unknown command,
// every table case is a recorded golden, and the Go-only rows work.
func TestCLIParity(t *testing.T) {
	rows := loadParity(t)
	var index []cliGolden
	if err := json.Unmarshal(golden.ReadFile(t, "cli", "index.json"), &index); err != nil {
		t.Fatal(err)
	}
	recorded := map[string]bool{}
	for _, g := range index {
		recorded[g.Name] = true
	}
	ported := portedCommands(rows)
	registered := map[string]bool{}
	for _, c := range commands(imapclient.Opener, nil) {
		registered[c.Name] = true
	}
	for _, row := range rows {
		for _, c := range row.Cases {
			if !recorded[c.Name] {
				t.Errorf("%s: case %q is not in index.json", row.Command, c.Name)
			}
		}
		if len(row.Path) == 0 || row.Status == "go-only" {
			continue
		}
		if row.Status == "ported" && !registered[row.Path[0]] {
			t.Errorf("%s: ported but the binary has no %q command", row.Command, row.Path[0])
		}
		if row.Status == "excluded-until-ported" && len(row.Path) == 1 && row.Path[0] != "codemode-worker" && registered[row.Path[0]] {
			t.Errorf("%s: excluded but the binary registers %q", row.Command, row.Path[0])
		}
	}
	for name := range registered {
		if !ported[name] && name != "codemode-worker" {
			t.Errorf("binary command %q has no ported parity row", name)
		}
	}
	data := loadGoldenData(t)
	r := newRig(t, data, nil, "")
	for _, row := range rows {
		if row.Status != "ported" || len(row.Path) == 0 {
			continue
		}
		t.Run("help "+row.Command, func(t *testing.T) {
			code, stdout, stderr := r.run(context.Background(), append(slices.Clone(row.Path), "-h")...)
			if want := "usage: atrium " + row.Command; code != cli.ExitOK || !strings.HasPrefix(stdout, want) {
				t.Errorf("exit %d, stdout %q, stderr %q; want exit 0 and prefix %q", code, stdout, stderr, want)
			}
		})
	}
	for _, row := range rows {
		if row.Status != "excluded-until-ported" || len(row.Path) == 0 {
			continue
		}
		t.Run("excluded "+row.Command, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := newApp().Run(context.Background(), row.Path, &stdout, &stderr)
			if len(row.Path) > 1 && registered[row.Path[0]] {
				if want := "atrium " + row.Command + " is not ported in v2"; code != cli.ExitError || !strings.Contains(stderr.String(), want) {
					t.Errorf("exit %d, stderr %q; want exit 1 and %q", code, stderr.String(), want)
				}
				return
			}
			if row.Path[0] == "codemode-worker" {
				if code != cli.ExitError || !strings.Contains(stderr.String(), "removed in v2") {
					t.Errorf("exit %d, stderr %q; want exit 1 and the removed-in-v2 message", code, stderr.String())
				}
				return
			}
			refused := row.Path[0]
			if registered[refused] && len(row.Path) > 1 {
				refused = row.Path[1]
			}
			if want := "invalid choice: '" + refused + "'"; code != cli.ExitUsage || !strings.Contains(stderr.String(), want) {
				t.Errorf("exit %d, stderr %q; want exit 2 and %q", code, stderr.String(), want)
			}
		})
	}
	t.Run("go-only --version", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := newApp().Run(context.Background(), []string{"--version"}, &stdout, &stderr)
		if want := "atrium " + version + "\n"; code != cli.ExitOK || stdout.String() != want {
			t.Errorf("exit %d, stdout %q; want exit 0 and %q", code, stdout.String(), want)
		}
	})
	t.Run("go-only /healthz", func(t *testing.T) {
		resp, err := http.Get(startServe(t) + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status %d", resp.StatusCode)
		}
	})
}

func goldenEnv(t *testing.T, g cliGolden) map[string]string {
	t.Helper()
	if len(g.Argv) < 2 || g.Argv[1] != "media" {
		return g.Env
	}
	return mediaServices(t)
}

func mediaServices(t *testing.T) map[string]string {
	t.Helper()
	release := func(hash, title, size string) map[string]any {
		return map[string]any{"hash": hash, "title": title, "magnet_url": "magnet:?xt=urn:btih:" + hash + "&dn=x", "size_bytes": size, "season": "1", "episode": "1"}
	}
	releases := map[string][]map[string]any{
		"1234567": {
			release(strings.Repeat("a", 40), "Lanterns S01E07 1080p WEB h264", "1500000000"),
			release(strings.Repeat("c", 40), "Lanterns S01E08 1080p WEB h264", "2500000000"),
			release(strings.Repeat("d", 40), "Lanterns S01E09 1080p WEB h264", "2600000000"),
		},
		"7654321": {},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/get-torrents", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"torrents": releases[r.URL.Query().Get("imdb_id")]})
	})
	mux.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "Ok.") })
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "[]") })
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) {})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	previous := media.EZTVAPI
	media.EZTVAPI = srv.URL + "/api/get-torrents"
	t.Cleanup(func() { media.EZTVAPI = previous })
	return map[string]string{"QBITTORRENT_URL": srv.URL, "NTFY_URL": srv.URL}
}
