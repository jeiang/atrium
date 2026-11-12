package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/testutil/mailscript"
)

type classifiedMail struct {
	id      int64
	subject string
}

type mailClassifierRig struct {
	f       *fixture
	d       *db.DB
	targets []classifiedMail

	mu   sync.Mutex
	sent []anthropictest.Request
	hook func(anthropictest.Request)
}

func newMailClassifierRig(t *testing.T, answer func(required []string, user string) (json.RawMessage, error)) *mailClassifierRig {
	t.Helper()
	f := newFixture(t, loadGoldenData(t), map[string]string{"ATRIUM_MODE": "shadow"}, "")
	script := mailscript.Load(t)
	r := &mailClassifierRig{f: f, d: mustConnect(t, filepath.Join(f.dir, "atrium.db"), keyFor(t))}
	for _, q := range []string{
		"UPDATE messages SET seen = 0, sent = 0, in_inbox = 1, gone_at = NULL WHERE account = 'icloud' AND id IN (SELECT id FROM messages WHERE account = 'icloud' ORDER BY id LIMIT 3)",
		"DELETE FROM flag_decisions WHERE message_id IN (SELECT id FROM messages WHERE account = 'icloud' ORDER BY id LIMIT 3)",
		"DELETE FROM event_candidates",
	} {
		if _, err := r.d.Write.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := r.d.Read.Query("SELECT id, subject FROM messages WHERE account = 'icloud' ORDER BY id LIMIT 3")
	if err != nil {
		t.Fatal(err)
	}
	r.targets, err = db.Collect(rows, func(rows *sql.Rows) (classifiedMail, error) {
		var m classifiedMail
		return m, rows.Scan(&m.id, &m.subject)
	})
	if err != nil || len(r.targets) != 3 {
		t.Fatalf("target messages: %v %v", r.targets, err)
	}
	if answer == nil {
		answer = script.JSONAnswer
	}
	f.api.AnswerJSON(answer)
	f.api.SetOnRequest(func(req anthropictest.Request) {
		r.mu.Lock()
		r.sent = append(r.sent, req)
		hook := r.hook
		r.mu.Unlock()
		if hook != nil {
			hook(req)
		}
	})
	return r
}

type sentClassification struct {
	caller, subject string
	body            map[string]any
}

func (r *mailClassifierRig) requests(t *testing.T) []sentClassification {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sentClassification, 0, len(r.sent))
	for _, req := range r.sent {
		body := req.JSON(t)
		out = append(out, sentClassification{caller: callerOf(t, body), subject: subjectIn(body), body: body})
	}
	return out
}

func callerOf(t *testing.T, body map[string]any) string {
	t.Helper()
	schema, _ := body["output_config"].(map[string]any)["format"].(map[string]any)["schema"].(map[string]any)
	required, _ := schema["required"].([]any)
	keys := make([]string, 0, len(required))
	for _, k := range required {
		keys = append(keys, fmt.Sprint(k))
	}
	slices.Sort(keys)
	switch strings.Join(keys, ",") {
	case "deadline_or_payment_due,flag,person_asking,security_event":
		return "flag"
	case "has_event":
		return "event"
	}
	return strings.Join(keys, ",")
}

func userText(body map[string]any) string {
	messages, _ := body["messages"].([]any)
	if len(messages) != 1 {
		return ""
	}
	text, _ := messages[0].(map[string]any)["content"].(string)
	return text
}

func subjectIn(body map[string]any) string {
	for line := range strings.SplitSeq(userText(body), "\n") {
		if subject, ok := strings.CutPrefix(line, "Subject: "); ok {
			return subject
		}
	}
	return ""
}

func (r *mailClassifierRig) targetCalls(t *testing.T, from int) map[string][]string {
	t.Helper()
	calls := map[string][]string{}
	subjects := map[string]bool{}
	for _, m := range r.targets {
		subjects[m.subject] = true
	}
	for _, req := range r.requests(t)[from:] {
		if subjects[req.subject] {
			calls[req.caller] = append(calls[req.caller], req.subject)
		}
	}
	return calls
}

func (r *mailClassifierRig) sync(t *testing.T) (int, string) {
	t.Helper()
	code, _, stderr := r.f.Run(context.Background(), "sync", "--account", "icloud")
	return code, stderr
}

type flagRow struct {
	present  bool
	contacts int
	llm      sql.NullInt64
	asking   sql.NullInt64
}

func (r *mailClassifierRig) flag(t *testing.T, id int64) flagRow {
	t.Helper()
	var row flagRow
	err := r.d.Read.QueryRow("SELECT contacts_match, person_asking, llm_flag FROM flag_decisions WHERE message_id = ?", id).
		Scan(&row.contacts, &row.asking, &row.llm)
	if err == sql.ErrNoRows {
		return row
	}
	if err != nil {
		t.Fatal(err)
	}
	row.present = true
	return row
}

func (r *mailClassifierRig) event(t *testing.T, id int64) string {
	t.Helper()
	var status string
	err := r.d.Read.QueryRow("SELECT status FROM event_candidates WHERE message_id = ?", id).Scan(&status)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func (r *mailClassifierRig) noMailWrites(t *testing.T) {
	t.Helper()
	if writes := r.f.fakes["icloud"].Writes; len(writes) != 0 {
		t.Fatalf("the pass wrote to the mailbox: %+v", writes)
	}
}

func scriptedAnswers(t *testing.T, script *mailscript.Script, flags map[string]string, events map[string]string) func([]string, string) (json.RawMessage, error) {
	t.Helper()
	return func(required []string, user string) (json.RawMessage, error) {
		subject := subjectIn(map[string]any{"messages": []any{map[string]any{"content": user}}})
		switch {
		case slices.Contains(required, "flag") && flags[subject] != "":
			return json.RawMessage(flags[subject]), nil
		case slices.Contains(required, "has_event") && events[subject] != "":
			return json.RawMessage(events[subject]), nil
		}
		return script.JSONAnswer(required, user)
	}
}

const (
	flagYes = `{"person_asking":true,"deadline_or_payment_due":false,"security_event":false,"flag":true}`
	flagNo  = `{"person_asking":false,"deadline_or_payment_due":false,"security_event":false,"flag":false}`
)

func TestMailPassFlagsAndEventsThroughTheClassifier(t *testing.T) {
	t.Parallel()
	flags, events := map[string]string{}, map[string]string{}
	script := mailscript.Load(t)
	r := newMailClassifierRig(t, scriptedAnswers(t, script, flags, events))
	a, b, c := r.targets[0], r.targets[1], r.targets[2]
	flags[a.subject], flags[b.subject], flags[c.subject] = flagYes, flagNo, flagNo
	events[a.subject], events[b.subject], events[c.subject] = `{"has_event":true}`, `{"has_event":false}`, `{"has_event":false}`

	if code, stderr := r.sync(t); code != cli.ExitOK {
		t.Fatalf("sync: exit %d stderr %q", code, stderr)
	}
	if got := r.flag(t, a.id); !got.llm.Valid || got.llm.Int64 != 1 || got.asking.Int64 != 1 {
		t.Errorf("positive flag judgment for %q: %+v", a.subject, got)
	}
	for _, m := range []classifiedMail{b, c} {
		if got := r.flag(t, m.id); !got.present || !got.llm.Valid || got.llm.Int64 != 0 || !got.asking.Valid || got.asking.Int64 != 0 {
			t.Errorf("negative flag judgment for %q is not a stored false: %+v", m.subject, got)
		}
	}
	if got := r.event(t, a.id); got != "queued" {
		t.Errorf("event candidate for %q: %q", a.subject, got)
	}
	for _, m := range []classifiedMail{b, c} {
		if got := r.event(t, m.id); got != "none" {
			t.Errorf("event judgment for %q: %q", m.subject, got)
		}
	}
	calls := r.targetCalls(t, 0)
	if len(calls["flag"]) != 3 || len(calls["event"]) != 3 {
		t.Fatalf("classifier calls for the three messages: %v", calls)
	}
	for _, req := range r.requests(t) {
		if req.caller != "flag" && req.caller != "event" {
			continue
		}
		body := req.body
		if body["model"] != pricing.HaikuID || body["max_tokens"] != float64(80) {
			t.Errorf("%s request model/max_tokens: %v %v", req.caller, body["model"], body["max_tokens"])
		}
		for _, key := range []string{"tools", "tool_choice", "thinking"} {
			if _, ok := body[key]; ok {
				t.Errorf("%s request carries %s", req.caller, key)
			}
		}
		if format := body["output_config"].(map[string]any)["format"].(map[string]any); format["type"] != "json_schema" {
			t.Errorf("%s output format: %v", req.caller, format)
		}
		if messages := body["messages"].([]any); len(messages) != 1 {
			t.Errorf("%s request has %d messages, want only the message to judge", req.caller, len(messages))
		}
		text := userText(body)
		head, rest, ok := strings.Cut(text, "\nBody:\n")
		if !ok || !strings.HasPrefix(head, "From: ") || len([]rune(rest)) > 1500 {
			t.Errorf("%s user prompt shape: %q", req.caller, text)
		}
		system, _ := body["system"].(string)
		if req.caller == "flag" && !strings.HasPrefix(system, "You triage the mailbox owner's email") ||
			req.caller == "event" && !strings.HasPrefix(system, "You scan the mailbox owner's email for calendar events") {
			t.Errorf("%s system prompt: %q", req.caller, system)
		}
	}
	var settled int
	if err := r.d.Read.QueryRow("SELECT COUNT(*) FROM usage WHERE source IN ('classifier:mail_flag', 'classifier:mail_event') AND state = 'settled'").Scan(&settled); err != nil || settled < 6 {
		t.Errorf("settled classifier usage rows: %d %v", settled, err)
	}
	r.noMailWrites(t)

	before := len(r.requests(t))
	if code, stderr := r.sync(t); code != cli.ExitOK {
		t.Fatalf("second sync: exit %d stderr %q", code, stderr)
	}
	if calls := r.targetCalls(t, before); len(calls) != 0 {
		t.Errorf("a judged message was judged again: %v", calls)
	}
}

func TestMailPassBudgetRefusalDefersPaidStagesUntilRaised(t *testing.T) {
	t.Parallel()
	r := newMailClassifierRig(t, nil)
	a, b, c := r.targets[0], r.targets[1], r.targets[2]
	if _, err := r.d.Write.Exec("DELETE FROM mail_scans"); err != nil {
		t.Fatal(err)
	}
	scans := func(where string) int {
		t.Helper()
		var n int
		if err := r.d.Read.QueryRow("SELECT COUNT(*) FROM mail_scans WHERE " + where).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	var once sync.Once
	r.mu.Lock()
	r.hook = func(req anthropictest.Request) {
		if callerOf(t, req.JSON(t)) != "flag" {
			return
		}
		once.Do(func() {
			if _, err := r.d.Write.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('monthly_budget_usd', '0.000001')"); err != nil {
				t.Error(err)
			}
		})
	}
	r.mu.Unlock()

	if code, stderr := r.sync(t); code != cli.ExitOK {
		t.Fatalf("sync with the budget exhausted mid-pass: exit %d stderr %q", code, stderr)
	}
	calls := r.targetCalls(t, 0)
	if !slices.Equal(calls["flag"], []string{a.subject}) || len(calls["event"]) != 0 {
		t.Fatalf("paid calls after the budget refusal: %v", calls)
	}
	if got := r.flag(t, a.id); !got.llm.Valid {
		t.Errorf("the judged message lost its flag judgment: %+v", got)
	}
	for _, m := range []classifiedMail{b, c} {
		if got := r.flag(t, m.id); !got.present || got.llm.Valid || got.asking.Valid {
			t.Errorf("deferred message %q has a model flag judgment: %+v", m.subject, got)
		}
	}
	for _, m := range r.targets {
		if got := r.event(t, m.id); got != "" {
			t.Errorf("deferred message %q has an event status %q", m.subject, got)
		}
	}
	var caller, item string
	if err := r.d.Read.QueryRow("SELECT last_caller, last_item_key FROM inference_classification_deferrals").Scan(&caller, &item); err != nil ||
		caller != "mail_flag" || item != fmt.Sprintf("icloud:%d", b.id) {
		t.Errorf("recorded deferral: %q %q %v", caller, item, err)
	}
	if sent := r.requests(t); len(sent) != 1 {
		t.Errorf("paid requests in the deferred pass = %d, want only the first flag", len(sent))
	}
	if n := scans("kind = 'commitments'"); n != 0 {
		t.Errorf("deferred commitment scans left %d markers", n)
	}
	if n := scans("kind = 'packages' AND found = 0"); n != 0 {
		t.Errorf("deferred model-only package scans left %d markers", n)
	}
	regexFound := scans("kind = 'packages' AND found = 1")
	if regexFound == 0 {
		t.Errorf("the package regex found nothing after the paid stages stopped")
	}
	r.noMailWrites(t)

	r.mu.Lock()
	r.hook = nil
	r.mu.Unlock()
	if code, _, stderr := r.f.Run(context.Background(), "settings", "set", "monthly_budget_usd", "200"); code != cli.ExitOK {
		t.Fatalf("raise the budget: exit %d stderr %q", code, stderr)
	}
	before := len(r.requests(t))
	if code, stderr := r.sync(t); code != cli.ExitOK {
		t.Fatalf("sync after raising the budget: exit %d stderr %q", code, stderr)
	}
	calls = r.targetCalls(t, before)
	slices.Sort(calls["flag"])
	want := []string{b.subject, c.subject}
	slices.Sort(want)
	if !slices.Equal(calls["flag"], want) || len(calls["event"]) != 3 {
		t.Fatalf("retry after raising the budget: %v", calls)
	}
	for _, m := range []classifiedMail{b, c} {
		if got := r.flag(t, m.id); !got.llm.Valid || !got.asking.Valid {
			t.Errorf("deferred message %q was not judged on the retry: %+v", m.subject, got)
		}
	}
	for _, m := range r.targets {
		if got := r.event(t, m.id); got == "" {
			t.Errorf("deferred message %q has no event judgment after the retry", m.subject)
		}
	}
	seen := map[string]map[string]bool{"commitments": {}, "packages": {}}
	for _, s := range r.requests(t)[before:] {
		kind := map[string]string{"commitments": "commitments"}[s.caller]
		if strings.Contains(s.caller, "tracking_number") {
			kind = "packages"
		}
		if kind == "" {
			continue
		}
		user := userText(s.body)
		if seen[kind][user] {
			t.Errorf("%s scan judged one message twice", kind)
		}
		seen[kind][user] = true
	}
	if n := scans("kind = 'commitments'"); n != len(seen["commitments"]) {
		t.Errorf("commitment markers = %d for %d commitment requests", n, len(seen["commitments"]))
	}
	if len(seen["packages"]) == 0 || scans("kind = 'packages'") < regexFound+len(seen["packages"]) {
		t.Errorf("package retry: %d requests, %d markers, %d regex finds", len(seen["packages"]), scans("kind = 'packages'"), regexFound)
	}
	r.noMailWrites(t)
}

func TestMailPassMalformedFlagReplyMakesNoDecision(t *testing.T) {
	t.Parallel()
	flags := map[string]string{}
	script := mailscript.Load(t)
	r := newMailClassifierRig(t, scriptedAnswers(t, script, flags, map[string]string{}))
	a := r.targets[0]
	flags[a.subject] = `{"person_asking":"yes","deadline_or_payment_due":false,"security_event":false,"flag":true}`

	code, stderr := r.sync(t)
	if code == cli.ExitOK {
		t.Fatalf("sync with a malformed flag reply succeeded: stderr %q", stderr)
	}
	if got := r.flag(t, a.id); got.present {
		t.Errorf("a malformed reply produced a flag decision: %+v", got)
	}
	for _, m := range r.targets {
		if got := r.event(t, m.id); got != "" {
			t.Errorf("message %q has an event status %q after the malformed reply", m.subject, got)
		}
	}
	var charged int
	if err := r.d.Read.QueryRow("SELECT COUNT(*) FROM usage WHERE source = 'classifier:mail_flag' AND state = 'settled'").Scan(&charged); err != nil || charged != 1 {
		t.Errorf("settled usage for the malformed reply: %d %v", charged, err)
	}
	r.noMailWrites(t)
}
