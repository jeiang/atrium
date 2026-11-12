package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/testutil/embedstub"
	"github.com/jeiang/atrium/internal/testutil/mailscript"
)

const folderCaller = "confident,folder"

type routedMail struct {
	subject string
	uid     uint32
	id      int64
}

type routingRig struct {
	*mailSafety
	targets []routedMail
	local   *chatFeatures
}

func newRoutingRig(t *testing.T, mode string, answer func(required []string, user string) (json.RawMessage, error), subjects ...string) *routingRig {
	t.Helper()
	m := mailSafetyFixture(t, mode)
	local := &chatFeatures{}
	server := mailscript.NewOpenAI(t, mailscript.Load(t), func(feature string, _ []byte) bool {
		local.mu.Lock()
		defer local.mu.Unlock()
		local.seen = append(local.seen, feature)
		return true
	}).Serve()
	setEnv(t, "ATRIUM_MAIL_MAIL_CHAT_URL", server.URL+"/v1")
	if answer != nil {
		m.api.AnswerJSON(answer)
	}
	r := &routingRig{mailSafety: m, local: local}
	for i, subject := range subjects {
		r.targets = append(r.targets, routedMail{subject: subject,
			uid: m.add("INBOX", subject, fmt.Sprintf("orders%d@shop%d.test", i, i), fmt.Sprintf("target%d", i), "Thanks for your order.")})
	}
	m.add("Bills", "Statement 8", "bills@bank.test", "s8", "Filed message.")
	m.add("Receipts", "Receipt 7", "shop@store.test", "s7", "Filed message.")
	for i := 1; i <= 10; i++ {
		m.add("Bills", fmt.Sprintf("Filed %d", i), fmt.Sprintf("f%d@filler%d.test", i, i), fmt.Sprintf("f%d", i), "Filed message.")
		m.add("Receipts", fmt.Sprintf("Kept %d", i), fmt.Sprintf("k%d@keeper%d.test", i, i), fmt.Sprintf("k%d", i), "Filed message.")
	}
	m.run(t, "sync")
	ids := []int64{m.messageID(t, "Statement 8"), m.messageID(t, "Receipt 7")}
	for i := range r.targets {
		r.targets[i].id = m.messageID(t, r.targets[i].subject)
		ids = append(ids, r.targets[i].id)
	}
	m.embed(t, ids...)
	if mode == "live" {
		m.run(t, "live", "rebaseline")
	}
	for _, target := range r.targets {
		m.fake.Boxes["INBOX"].Box.SetFlags(target.uid, []string{`\Seen`}, nil)
	}
	return r
}

func (r *routingRig) folderRequests(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, req := range r.api.Requests() {
		if body := req.JSON(t); callerOf(t, body) == folderCaller {
			out = append(out, body)
		}
	}
	return out
}

func (r *routingRig) decisions(t *testing.T, id int64) int {
	t.Helper()
	return r.count(t, "SELECT COUNT(*) FROM decisions WHERE message_id = ?", id)
}

func (r *routingRig) noLocalFolderJudgment(t *testing.T) {
	t.Helper()
	if features := r.local.features(); slices.Contains(features, "folder") {
		t.Fatalf("the local chat server answered a folder judgment: %v", features)
	}
}

func folderAnswers(t *testing.T, folders map[string]string) func([]string, string) (json.RawMessage, error) {
	t.Helper()
	script := mailscript.Load(t)
	return func(required []string, user string) (json.RawMessage, error) {
		if subject := subjectIn(map[string]any{"messages": []any{map[string]any{"content": user}}}); slices.Contains(required, "folder") && folders[subject] != "" {
			return json.RawMessage(folders[subject]), nil
		}
		return script.JSONAnswer(required, user)
	}
}

func TestMailPassRoutesThroughTheClassifier(t *testing.T) {
	r := newRoutingRig(t, "shadow", nil, "Order confirmation 55123")
	target := r.targets[0]
	r.run(t, "sync")

	if stage, action, dest := r.decision(t, target.id); stage != "llm" || action != "file" || dest != "Receipts" {
		t.Fatalf("decision = %s/%s/%s, want llm/file/Receipts", stage, action, dest)
	}
	if key := r.scalar(t, "SELECT key FROM decisions WHERE message_id = ?", target.id); key != "chat" {
		t.Errorf("stored decision key = %s, want chat", key)
	}
	if detail := r.scalar(t, "SELECT detail FROM decisions WHERE message_id = ?", target.id); !strings.Contains(detail, `"confident":true`) {
		t.Errorf("stored decision detail = %s", detail)
	}
	if got := r.verbs(); len(got) != 0 {
		t.Fatalf("shadow routing wrote to IMAP: %v", got)
	}
	if n := r.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'mail_auto'"); n != 0 {
		t.Fatalf("shadow routing recorded %d automatic actions", n)
	}
	requests := r.folderRequests(t)
	if len(requests) != 1 {
		t.Fatalf("folder classifications = %d, want 1", len(requests))
	}
	body := requests[0]
	if body["model"] != pricing.HaikuID || body["max_tokens"] != float64(80) {
		t.Errorf("folder request model/max_tokens: %v %v", body["model"], body["max_tokens"])
	}
	for _, key := range []string{"tools", "tool_choice", "thinking"} {
		if _, ok := body[key]; ok {
			t.Errorf("folder request carries %s", key)
		}
	}
	format := body["output_config"].(map[string]any)["format"].(map[string]any)
	enum := format["schema"].(map[string]any)["properties"].(map[string]any)["folder"].(map[string]any)["enum"]
	if format["type"] != "json_schema" || fmt.Sprint(enum) != "[Bills Receipts uncertain]" {
		t.Errorf("folder output format %v with folder enum %v", format["type"], enum)
	}
	if messages := body["messages"].([]any); len(messages) != 1 {
		t.Errorf("folder request has %d messages", len(messages))
	}
	if system, _ := body["system"].(string); !strings.HasPrefix(system, "You file email into mail folders") {
		t.Errorf("folder system prompt: %q", system)
	}
	text := userText(body)
	head, rest, ok := strings.Cut(text, "\nBody:\n")
	if !ok || !strings.HasPrefix(head, "Allowed folders:\n- Bills (top senders: ") || !strings.Contains(head, "\n- Receipts (top senders: ") ||
		!strings.Contains(head, "Similar filed messages (folder in brackets):\n1. [") ||
		!strings.Contains(head, "Message to file:\nSubject: Order confirmation 55123\nFrom: ") || len([]rune(rest)) > 1500 {
		t.Errorf("folder user prompt: %q", text)
	}
	if n := r.count(t, "SELECT COUNT(*) FROM usage WHERE source = 'classifier:mail_folder' AND state = 'settled'"); n != 1 {
		t.Errorf("settled folder classification usage rows = %d, want 1", n)
	}
	r.noLocalFolderJudgment(t)

	r.run(t, "sync")
	if n := len(r.folderRequests(t)); n != 1 {
		t.Errorf("a routed message was classified again: %d folder requests", n)
	}
}

func TestMailPassRefusesAFolderOutsideTheCatalog(t *testing.T) {
	const subject = "Order confirmation 55123"
	r := newRoutingRig(t, "shadow", folderAnswers(t, map[string]string{subject: `{"folder":"Elsewhere","confident":true}`}), subject)
	target := r.targets[0]
	ran := runApp(t, "sync")
	if ran.code == cli.ExitOK {
		t.Fatalf("a folder outside the catalog was accepted: stderr %q", ran.stderr)
	}
	if n := r.decisions(t, target.id); n != 0 {
		t.Fatalf("the refused folder produced %d decisions", n)
	}
	if n := r.count(t, "SELECT COUNT(*) FROM usage WHERE source = 'classifier:mail_folder' AND state = 'settled'"); n != 1 {
		t.Errorf("the refused reply was charged %d times, want once", n)
	}
	if got := r.verbs(); len(got) != 0 {
		t.Fatalf("the refused folder wrote to IMAP: %v", got)
	}
	if n := r.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'mail_auto'"); n != 0 {
		t.Fatalf("the refused folder recorded %d automatic actions", n)
	}
	r.noLocalFolderJudgment(t)
}

func TestMailPassUncertainFolderKeepsTheFallback(t *testing.T) {
	const subject = "Mystery message"
	r := newRoutingRig(t, "shadow", nil, subject)
	r.run(t, "sync")
	if stage, action, dest := r.decision(t, r.targets[0].id); stage != "fallback" || action != "file" || dest != "Misc" {
		t.Fatalf("decision after an uncertain answer = %s/%s/%s, want fallback/file/Misc", stage, action, dest)
	}
	if n := len(r.folderRequests(t)); n != 1 {
		t.Errorf("folder classifications = %d, want 1", n)
	}
}

func receiptsFor(t *testing.T, subjects ...string) func([]string, string) (json.RawMessage, error) {
	t.Helper()
	folders := map[string]string{}
	for _, subject := range subjects {
		folders[subject] = `{"folder":"Receipts","confident":true}`
	}
	return folderAnswers(t, folders)
}

func TestMailPassFolderBudgetRefusalDefersUntilRaised(t *testing.T) {
	answer := receiptsFor(t, "Order confirmation 55124", "Order confirmation 55125")
	var once sync.Once
	var r *routingRig
	r = newRoutingRig(t, "shadow", func(required []string, user string) (json.RawMessage, error) {
		if slices.Contains(required, "folder") {
			once.Do(func() {
				r.exec(t, "INSERT OR REPLACE INTO settings (key, value) VALUES ('monthly_budget_usd', '0.000001')")
			})
		}
		return answer(required, user)
	}, "Order confirmation 55123", "Order confirmation 55124", "Order confirmation 55125")
	first, deferred := r.targets[0], r.targets[1:]
	r.run(t, "sync")

	if stage, _, dest := r.decision(t, first.id); stage != "llm" || dest != "Receipts" {
		t.Fatalf("the first message routed %s/%s, want llm/Receipts", stage, dest)
	}
	for _, m := range deferred {
		if n := r.decisions(t, m.id); n != 0 {
			t.Fatalf("deferred message %q has %d decisions, want none and no fallback", m.subject, n)
		}
	}
	if n := len(r.folderRequests(t)); n != 1 {
		t.Fatalf("folder requests after the budget refusal = %d, want 1", n)
	}
	if got := r.scalar(t, "SELECT last_caller || ' ' || last_item_key FROM inference_classification_deferrals"); got != "mail_folder icloud:"+strconv.FormatInt(deferred[0].id, 10) {
		t.Errorf("recorded deferral = %s", got)
	}
	if got := r.verbs(); len(got) != 0 {
		t.Fatalf("the deferred pass wrote to IMAP: %v", got)
	}

	r.run(t, "settings", "set", "monthly_budget_usd", "200")
	r.run(t, "sync")
	for _, m := range deferred {
		if stage, _, dest := r.decision(t, m.id); stage != "llm" || dest != "Receipts" {
			t.Errorf("deferred message %q routed %s/%s after raising the budget", m.subject, stage, dest)
		}
	}
	if n := len(r.folderRequests(t)); n != 3 {
		t.Errorf("folder requests after raising the budget = %d, want 3", n)
	}
	if got := r.verbs(); len(got) != 0 {
		t.Fatalf("shadow routing wrote to IMAP: %v", got)
	}
	r.noLocalFolderJudgment(t)
}

func TestMailClassifiedRouteKeepsTheLiveGate(t *testing.T) {
	t.Run("paused", func(t *testing.T) {
		r := newRoutingRig(t, "live", nil, "Order confirmation 55123")
		r.run(t, "live", "pause")
		r.run(t, "sync")
		if stage, _, dest := r.decision(t, r.targets[0].id); stage != "llm" || dest != "Receipts" {
			t.Fatalf("decision = %s/%s, want llm/Receipts", stage, dest)
		}
		if got := r.verbs(); len(got) != 0 {
			t.Fatalf("a paused pass wrote to IMAP: %v", got)
		}
		if n := r.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'mail_auto'"); n != 0 {
			t.Fatalf("a paused pass recorded %d automatic actions", n)
		}
		r.run(t, "live", "resume")
		r.run(t, "sync")
		if got := r.verbs(); len(got) != 3 || got[0] != "COPY" || got[1] != "STORE" || got[2] != "EXPUNGE" {
			t.Fatalf("after resume the write log = %v, want COPY, STORE, EXPUNGE", got)
		}
		if n := len(r.fake.Boxes["Receipts"].Box.Messages); n != 12 {
			t.Errorf("Receipts holds %d messages after the move, want 12", n)
		}
		if n := len(r.folderRequests(t)); n != 1 {
			t.Errorf("folder requests = %d, want 1", n)
		}
	})

	t.Run("cap", func(t *testing.T) {
		r := newRoutingRig(t, "live", receiptsFor(t, "Order confirmation 55124"), "Order confirmation 55123", "Order confirmation 55124")
		seedSettings(t, map[string]string{mail.KeyWriteCap: "1"})
		r.run(t, "sync")
		if n := r.copies(); n != 1 {
			t.Fatalf("with a cap of one the pass made %d copies, want 1", n)
		}
		if n := r.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'mail_auto' AND state = 'done'"); n != 1 {
			t.Fatalf("with a cap of one %d actions applied, want 1", n)
		}
		seedSettings(t, map[string]string{mail.KeyWriteCap: "2"})
		r.run(t, "sync")
		if n := r.copies(); n != 2 {
			t.Fatalf("after raising the cap the pass made %d copies, want 2", n)
		}
		if n := len(r.folderRequests(t)); n != 2 {
			t.Errorf("folder requests = %d, want 2", n)
		}
	})
}

func TestMailReplayModelClassifiesOnADisposableCopy(t *testing.T) {
	t.Parallel()
	f := newFixture(t, loadGoldenData(t), nil, "")
	dbPath := filepath.Join(f.dir, "atrium.db")
	before := fileDigest(t, dbPath)

	var imapOpens, embedCalls atomic.Int64
	clients := func(config.Config) mail.OpenFunc {
		return func(context.Context, mail.OpenOptions) (mail.Client, error) {
			imapOpens.Add(1)
			return nil, errors.New("replay opened IMAP")
		}
	}
	embed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		embedCalls.Add(1)
		embedstub.Handler(w, r)
	}))
	t.Cleanup(embed.Close)
	var mu sync.Mutex
	var sent []anthropictest.Request
	f.api.SetOnRequest(func(req anthropictest.Request) {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, req)
	})
	requests := func() []anthropictest.Request {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(sent)
	}
	env := f.snapshotEnv()
	env[anthropic.BaseURLEnv] = f.api.URL
	env["ATRIUM_HOST_CONFIG"] = hostConfigFrom(t, env["ATRIUM_HOST_CONFIG"], f.dir, "host-replay-model.toml", map[string]string{
		`url = ` + strconv.Quote(f.embedURL): `url = ` + strconv.Quote(embed.URL+"/v1"),
	})
	run := func(args ...string) appRun {
		var out, errs bytes.Buffer
		code := newAppWith(config.Map(maps.Clone(env)), clients, nil, quietLog).Run(context.Background(), args, &out, &errs)
		return appRun{code, out.String(), errs.String()}
	}

	plain := run("replay")
	if plain.code != cli.ExitOK {
		t.Fatalf("replay: exit %d stderr %q", plain.code, plain.stderr)
	}
	if n := len(requests()); n != 0 {
		t.Fatalf("replay without --model sent %d paid requests", n)
	}
	model := run("replay", "--model")
	if model.code != cli.ExitOK {
		t.Fatalf("replay --model: exit %d stderr %q", model.code, model.stderr)
	}
	again := run("replay")
	if again.code != cli.ExitOK || again.stdout != plain.stdout || again.stderr != plain.stderr {
		t.Fatalf("replay without --model changed after a model replay:\nstdout %q\nstderr %q", again.stdout, again.stderr)
	}
	if after := fileDigest(t, dbPath); after != before {
		t.Errorf("replay --model changed the database: %s -> %s", before, after)
	}
	if n := imapOpens.Load(); n != 0 {
		t.Errorf("replay --model opened IMAP %d times", n)
	}
	if n := embedCalls.Load(); n != 0 {
		t.Errorf("replay --model called the embedding endpoint %d times", n)
	}

	base, withModel := parseReplay(t, plain.stdout), parseReplay(t, model.stdout)
	if len(base) != 26 || len(withModel) != 26 {
		t.Fatalf("replays reported %d and %d messages, want 26", len(base), len(withModel))
	}
	script := mailscript.Load(t)
	wanted := map[string]int{}
	for _, req := range requests() {
		body := req.JSON(t)
		if caller := callerOf(t, body); caller != folderCaller {
			t.Fatalf("replay --model sent a %s request", caller)
		}
		raw, err := script.JSONAnswer([]string{"confident", "folder"}, userText(body))
		if err != nil {
			t.Fatal(err)
		}
		var answer struct{ Folder string }
		if err := json.Unmarshal(raw, &answer); err != nil {
			t.Fatal(err)
		}
		if answer.Folder == "uncertain" {
			wanted["fallback"]++
		} else {
			wanted["llm "+answer.Folder]++
		}
	}
	asked, got := 0, map[string]int{}
	plainText, modelText := strings.Split(plain.stdout, "\n"), strings.Split(model.stdout, "\n")
	for i, line := range base {
		m := withModel[i]
		if line.Stage != "llm" {
			if plainText[i] != modelText[i] {
				t.Errorf("free-stage line changed under --model:\nplain %s\nmodel %s", plainText[i], modelText[i])
			}
			continue
		}
		asked++
		switch m.Stage {
		case "llm":
			if m.Action != "file" || m.Key == nil || *m.Key != "chat" || m.Dest == nil || m.Confidence != nil {
				t.Errorf("model-stage line %+v", m)
				continue
			}
			got["llm "+*m.Dest]++
		case "fallback":
			got["fallback"]++
		default:
			t.Errorf("an asked message replayed at %q under --model", m.Stage)
		}
	}
	if asked != 2 || len(requests()) != asked || fmt.Sprint(got) != fmt.Sprint(wanted) {
		t.Errorf("model replay: %d asked, %d requests, decisions %v, want the recorded answers %v", asked, len(requests()), got, wanted)
	}
	if !strings.Contains(model.stderr, "messages replayed: 26") {
		t.Errorf("model replay summary:\n%s", model.stderr)
	}
}
