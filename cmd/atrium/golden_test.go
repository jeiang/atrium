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

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/mail/rebalance"
	"github.com/jeiang/atrium/internal/mail/watches"
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
		case name == "notion":
			out = append(out, &cli.Command{Name: name, Commands: []*cli.Command{
				{Name: "sync", Setup: noop},
				{Name: "search", Setup: noop},
				{Name: "wiki-plan", Help: "draft one proposal that turns every database and page into the wiki page tree", Setup: noop},
			}})
		default:
			out = append(out, &cli.Command{Name: name, Setup: noop})
		}
	}
	return out
}

func noop(*cli.Args) cli.Handler { return func(*cli.Env) int { return cli.ExitOK } }

// ported is what cmd/atrium registers today and so what the goldens can check.
var ported = []string{"rules", "shadow-report", "reindex", "rebalance", "deletions", "gmail-labels", "live", "proposals", "loops", "watches"}

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
	ran := 0
	for _, g := range index {
		parser := strings.HasPrefix(g.Name, "argparse-")
		if !parser && !slices.Contains(ported, g.Argv[1]) {
			continue
		}
		ran++
		t.Run(g.Name, func(t *testing.T) {
			r := newRig(t, data, g.Env, g.Stdin)
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
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			code, stdout, stderr := r.run(ctx, argv...)
			if code != g.ExitCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, g.ExitCode, stderr)
			}
			if want := string(golden.ReadFile(t, "cli", g.Stdout)); stdout != want {
				t.Errorf("stdout = %q, want %q", stdout, want)
			}
			wantErr := string(golden.ReadFile(t, "cli", g.Stderr))
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
