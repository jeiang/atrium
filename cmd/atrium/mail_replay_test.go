package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/testutil/llmstub"
)

type replayLine struct {
	ID           int64    `json:"id"`
	Account      string   `json:"account"`
	Stage        string   `json:"stage"`
	Action       string   `json:"action"`
	Dest         *string  `json:"dest"`
	Key          *string  `json:"key"`
	Confidence   *float64 `json:"confidence"`
	StoredStage  string   `json:"stored_stage"`
	StoredAction string   `json:"stored_action"`
	StoredDest   *string  `json:"stored_dest"`
	Vector       bool     `json:"vector"`
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func parseReplay(t *testing.T, stdout string) []replayLine {
	t.Helper()
	var out []replayLine
	for _, text := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if text == "" {
			continue
		}
		var line replayLine
		if err := json.Unmarshal([]byte(text), &line); err != nil {
			t.Fatalf("replay line %q: %v", text, err)
		}
		out = append(out, line)
	}
	return out
}

func TestMailReplayReportsStoredDecisionsWithoutExternalCalls(t *testing.T) {
	t.Parallel()
	f := newFixture(t, loadGoldenData(t), nil, "")
	dbPath := filepath.Join(f.dir, "atrium.db")
	before := fileDigest(t, dbPath)

	var imapOpens, embedCalls, chatCalls atomic.Int64
	clients := func(config.Config) mail.OpenFunc {
		return func(context.Context, mail.OpenOptions) (mail.Client, error) {
			imapOpens.Add(1)
			return nil, errors.New("replay opened IMAP")
		}
	}
	srv := llmstub.Server(t, func(string, []byte) bool { return true })
	inner := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			embedCalls.Add(1)
		} else {
			chatCalls.Add(1)
		}
		inner.ServeHTTP(w, r)
	})
	env := f.snapshotEnv()
	env["ATRIUM_MAIL_MAIL_CHAT_URL"] = srv.URL + "/v1"
	env["ATRIUM_HOST_CONFIG"] = hostConfigFrom(t, env["ATRIUM_HOST_CONFIG"], f.dir, "host-replay.toml", map[string]string{
		`url = ` + strconv.Quote(f.llm): `url = ` + strconv.Quote(srv.URL+"/v1"),
	})
	appFor := func(path string) *cli.App {
		vars := maps.Clone(env)
		vars["ATRIUM_DB"] = path
		reg := settings.New()
		return newAppWith(config.Map(vars), reg, config.NewState(reg), clients, nil, quietLog)
	}
	run := func(app *cli.App, args ...string) appRun {
		var out, errs bytes.Buffer
		code := app.Run(context.Background(), args, &out, &errs)
		return appRun{code, out.String(), errs.String()}
	}

	first := run(appFor(dbPath), "replay")
	if first.code != cli.ExitOK {
		t.Fatalf("replay: exit %d stdout %q stderr %q", first.code, first.stdout, first.stderr)
	}
	second := run(appFor(dbPath), "replay")
	if second.code != cli.ExitOK {
		t.Fatalf("second replay: exit %d stderr %q", second.code, second.stderr)
	}
	if first.stdout != second.stdout || first.stderr != second.stderr {
		t.Fatalf("two replays differ:\nfirst stdout %q\nsecond stdout %q\nfirst stderr %q\nsecond stderr %q",
			first.stdout, second.stdout, first.stderr, second.stderr)
	}
	if after := fileDigest(t, dbPath); after != before {
		t.Errorf("replay changed the database: %s -> %s", before, after)
	}
	if n := imapOpens.Load(); n != 0 {
		t.Errorf("replay opened IMAP %d times", n)
	}
	if n := embedCalls.Load(); n != 0 {
		t.Errorf("replay called the embedding endpoint %d times", n)
	}
	if n := chatCalls.Load(); n != 0 {
		t.Errorf("replay called the chat endpoint %d times", n)
	}

	lines := parseReplay(t, first.stdout)
	if len(lines) != 26 {
		t.Fatalf("replay reported %d messages, want the 26 stored decisions:\n%s", len(lines), first.stdout)
	}
	keys := []string{`"id":`, `"account":`, `"stage":`, `"action":`, `"dest":`, `"key":`, `"confidence":`,
		`"stored_stage":`, `"stored_action":`, `"stored_dest":`, `"vector":`}
	firstLine := strings.SplitN(first.stdout, "\n", 2)[0]
	at := -1
	for _, key := range keys {
		next := strings.Index(firstLine, key)
		if next <= at {
			t.Errorf("the first replay line does not keep the key order at %s: %s", key, firstLine)
			break
		}
		at = next
	}
	stages := map[string]int{}
	for i, line := range lines {
		stages[line.Stage]++
		if i > 0 && line.ID <= lines[i-1].ID {
			t.Errorf("message %d follows %d; ids must ascend", line.ID, lines[i-1].ID)
		}
		if line.Confidence != nil {
			text := strconv.FormatFloat(*line.Confidence, 'f', -1, 64)
			if dot := strings.IndexByte(text, '.'); dot >= 0 && len(text)-dot-1 > 4 {
				t.Errorf("message %d confidence %s is not rounded to four decimals", line.ID, text)
			}
		}
	}
	for _, want := range []string{"correction", "operator", "learned", "llm", "none"} {
		if stages[want] == 0 {
			t.Errorf("no message replayed at stage %q; stages %v", want, stages)
		}
	}
	var asked *replayLine
	for i := range lines {
		if lines[i].Stage == "llm" {
			asked = &lines[i]
			break
		}
	}
	if asked == nil {
		t.Fatal("no message reached the llm stage")
	}
	if asked.Action != "ask" || asked.Dest != nil || asked.Key != nil || asked.Confidence != nil {
		t.Errorf("the llm stage asked with %+v; want action ask and no destination, key or confidence", *asked)
	}
	if asked.StoredStage == "llm" || asked.StoredAction == "ask" {
		t.Errorf("the llm message stored %q/%q; the model was off at replay time", asked.StoredStage, asked.StoredAction)
	}
	if !asked.Vector {
		t.Errorf("the llm message reports no stored vector")
	}
	var missing []replayLine
	for _, line := range lines {
		if !line.Vector {
			missing = append(missing, line)
		}
	}
	if len(missing) == 0 {
		t.Error("no message lacked a stored vector; the vector-skipping path is untested")
	}
	var skipped bool
	for _, line := range missing {
		if line.Stage == "knn" || line.Stage == "llm" {
			t.Errorf("message %d has no stored vector but replayed at the vector stage %q", line.ID, line.Stage)
		}
		if line.Stage == "none" {
			skipped = true
		}
	}
	if !skipped {
		t.Error("no message without a stored vector stopped before a decision")
	}
	for _, want := range []string{"messages replayed: 26", "differences: 11", "by stage: correction=2, operator=2, learned=11, knn=0, llm=2, fallback=0, none=9"} {
		if !strings.Contains(first.stderr, want) {
			t.Errorf("summary lacks %q:\n%s", want, first.stderr)
		}
	}

	one := run(appFor(dbPath), "replay", "--account", "gmail", "--limit", "1")
	if one.code != cli.ExitOK {
		t.Fatalf("replay --account gmail --limit 1: exit %d stderr %q", one.code, one.stderr)
	}
	limited := parseReplay(t, one.stdout)
	if len(limited) != 1 || limited[0].Account != "gmail" {
		t.Errorf("replay --account gmail --limit 1 reported %+v", limited)
	}
	if got := run(appFor(dbPath), "replay", "--account", "nobody"); got.code != cli.ExitUsage {
		t.Errorf("replay of an unknown account: exit %d stderr %q", got.code, got.stderr)
	}

	t.Run("without an accounts file", func(t *testing.T) {
		bare := filepath.Join(t.TempDir(), "atrium.db")
		copyFile(t, dbPath, bare)
		exec := func(query string) {
			t.Helper()
			d, err := db.Connect(bare)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.Write.Exec("PRAGMA foreign_keys = OFF"); err != nil {
				_ = d.Close()
				t.Fatal(err)
			}
			if _, err := d.Write.Exec(query); err != nil {
				_ = d.Close()
				t.Fatal(err)
			}
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
		}
		exec("DELETE FROM mail_accounts")
		fromRows := run(appFor(bare), "replay")
		if fromRows.code != cli.ExitOK {
			t.Fatalf("replay from the account rows: exit %d stderr %q", fromRows.code, fromRows.stderr)
		}
		if fromRows.stdout != first.stdout || fromRows.stderr != first.stderr {
			t.Errorf("a copy without mail_accounts replayed differently:\nstdout %q\nstderr %q", fromRows.stdout, fromRows.stderr)
		}
		exec("DELETE FROM accounts WHERE name = 'gmail'")
		unconfigured := run(appFor(bare), "replay")
		if unconfigured.code != cli.ExitOK {
			t.Fatalf("replay with an unconfigured account: exit %d stderr %q", unconfigured.code, unconfigured.stderr)
		}
		if !strings.Contains(unconfigured.stderr, `not routed: account "gmail" is not configured in the database (10 messages)`) {
			t.Errorf("the summary does not name the unconfigured account:\n%s", unconfigured.stderr)
		}
		for _, line := range parseReplay(t, unconfigured.stdout) {
			if line.Account == "gmail" && (line.Stage != "none" || line.Action != "none") {
				t.Errorf("gmail message %d replayed at %q/%q without an account row", line.ID, line.Stage, line.Action)
			}
		}
	})
}
