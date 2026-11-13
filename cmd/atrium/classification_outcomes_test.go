package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/mail/mailtest"
)

var stopReplies = []struct {
	name, reason string
	reply        func() anthropictest.Response
}{
	{"refusal", "refusal", func() anthropictest.Response { return anthropictest.Refusal("cyber", "declined") }},
	{"max_tokens", "truncated", func() anthropictest.Response {
		return anthropictest.Reply("max_tokens", anthropictest.Text(`{"`))
	}},
}

const ageFailures = "UPDATE runs SET started_at = started_at - 7200 WHERE kind = 'classification' AND state = 'failed'"

var accountingTables = map[string]bool{"runs": true, "threads": true, "thread_messages": true, "usage": true, "inference_classification_deferrals": true}

func domainDigests(t *testing.T, path string) map[string]string {
	t.Helper()
	d := mustConnect(t, path, keyFor(t))
	rows, err := d.Read.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'thread_fts%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !accountingTables[name] {
			names = append(names, name)
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, name := range names {
		rows, err := d.Read.Query(`SELECT * FROM "` + name + `" ORDER BY rowid`)
		if err != nil {
			rows, err = d.Read.Query(`SELECT * FROM "` + name + `"`)
		}
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			_, _ = fmt.Fprintf(h, "%v\n", values)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		out[name] = hex.EncodeToString(h.Sum(nil))
	}
	return out
}

func scanCaller(caller string) string {
	if strings.Contains(caller, "tracking_number") {
		return "packages"
	}
	return caller
}

func (r *mailClassifierRig) recentSent(t *testing.T) {
	t.Helper()
	if _, err := r.d.Write.Exec("UPDATE messages SET date_ts = CAST(strftime('%s', 'now') AS REAL) - 3600 " +
		"WHERE id IN (SELECT id FROM messages WHERE account = 'icloud' AND sent = 1 ORDER BY id LIMIT 3)"); err != nil {
		t.Fatal(err)
	}
}

func (r *mailClassifierRig) syncAll(t *testing.T) {
	t.Helper()
	if code, _, stderr := r.f.Run(context.Background(), "sync"); code != cli.ExitOK {
		t.Fatalf("sync: exit %d stderr %q", code, stderr)
	}
}

func (r *mailClassifierRig) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := r.d.Read.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (r *mailClassifierRig) failedItems(t *testing.T, reason string) map[string]int {
	t.Helper()
	rows, err := r.d.Read.Query("SELECT json_extract(limits_json, '$.item_key'), COUNT(*) FROM runs "+
		"WHERE kind = 'classification' AND state = 'failed' AND error = ? GROUP BY 1", reason)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			t.Fatal(err)
		}
		out[key] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMailPassStopReasonsArePerMessageOutcomes(t *testing.T) {
	t.Parallel()
	for _, caller := range []string{"flag", "event", "commitments", "packages"} {
		for _, stop := range stopReplies {
			t.Run(caller+"/"+stop.name, func(t *testing.T) {
				t.Parallel()
				r := newMailClassifierRig(t, nil)
				if _, err := r.d.Write.Exec("DELETE FROM mail_scans"); err != nil {
					t.Fatal(err)
				}
				r.recentSent(t)
				var mu sync.Mutex
				bad, firstPass := "", true
				var others []string
				r.mu.Lock()
				r.hook = func(req anthropictest.Request) {
					body := req.JSON(t)
					if scanCaller(callerOf(t, body)) != caller {
						return
					}
					user := userText(body)
					mu.Lock()
					defer mu.Unlock()
					if bad == "" {
						bad = user
					}
					if user == bad {
						r.f.api.PushJSON(stop.reply())
					} else if firstPass {
						others = append(others, user)
					}
				}
				r.mu.Unlock()

				r.syncAll(t)
				mu.Lock()
				firstPass = false
				afterFirst := len(others)
				mu.Unlock()
				if bad == "" {
					t.Fatalf("no %s classification was asked", caller)
				}
				if afterFirst == 0 {
					t.Errorf("the %s pass stopped paid work after the %s reply", caller, stop.name)
				}
				asked := func() int {
					n := 0
					for _, s := range r.requests(t) {
						if scanCaller(s.caller) == caller && userText(s.body) == bad {
							n++
						}
					}
					return n
				}
				r.syncAll(t)
				if n := asked(); n != 1 {
					t.Errorf("the %s item was asked %d times within the retry spacing, want 1", stop.name, n)
				}
				if _, err := r.d.Write.Exec(ageFailures); err != nil {
					t.Fatal(err)
				}
				r.syncAll(t)
				r.syncAll(t)
				if n := asked(); n != 2 {
					t.Errorf("the %s item was asked %d times over four passes, want 2", stop.name, n)
				}
				failed := r.failedItems(t, stop.reason)
				if len(failed) != 1 {
					t.Fatalf("failed %s runs by item: %v, want one item", stop.reason, failed)
				}
				var key string
				for k, n := range failed {
					key = k
					if n != 2 {
						t.Errorf("item %s has %d charged failures, want 2", k, n)
					}
				}
				id, err := strconv.ParseInt(key[strings.LastIndex(key, ":")+1:], 10, 64)
				if err != nil || !strings.HasPrefix(key, "icloud:") {
					t.Fatalf("item key %q", key)
				}
				switch caller {
				case "flag":
					if got := r.flag(t, id); !got.present || got.llm.Valid || got.asking.Valid {
						t.Errorf("the unanswered message has a model flag judgment: %+v", got)
					}
				case "event":
					if got := r.event(t, id); got != "none" {
						t.Errorf("the unanswered message has event status %q, want none", got)
					}
					if n := r.count(t, "SELECT COUNT(*) FROM event_candidates WHERE message_id = ? AND reason = 'model gave no usable answer'", id); n != 1 {
						t.Errorf("the unanswered message has %d event rows with the no-answer reason, want 1", n)
					}
				default:
					if n := r.count(t, "SELECT COUNT(*) FROM mail_scans WHERE kind = ? AND message_id = ? AND found = 0", caller, id); n != 1 {
						t.Errorf("the unanswered message has %d zero-found %s markers, want 1", n, caller)
					}
				}
				r.noMailWrites(t)
			})
		}
	}
}

func TestMailRefusedFlagDoesNotStopOtherMessagesOrAccounts(t *testing.T) {
	t.Parallel()
	r := newMailClassifierRig(t, nil)
	a, b, c := r.targets[0], r.targets[1], r.targets[2]
	r.mu.Lock()
	r.hook = func(req anthropictest.Request) {
		if body := req.JSON(t); callerOf(t, body) == "flag" && subjectIn(body) == a.subject {
			r.f.api.PushJSON(anthropictest.Refusal("cyber", "declined"))
		}
	}
	r.mu.Unlock()
	var lateID int64
	if err := r.d.Read.QueryRow("SELECT id FROM messages WHERE account = 'gmail' AND sent = 0 ORDER BY id LIMIT 1").Scan(&lateID); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"UPDATE messages SET seen = 0, in_inbox = 1, gone_at = NULL WHERE id = ?",
		"DELETE FROM flag_decisions WHERE message_id = ?",
	} {
		if _, err := r.d.Write.Exec(q, lateID); err != nil {
			t.Fatal(err)
		}
	}

	r.syncAll(t)
	for _, m := range []classifiedMail{b, c} {
		if got := r.flag(t, m.id); !got.llm.Valid {
			t.Errorf("message %q after the refused one has no model flag judgment: %+v", m.subject, got)
		}
	}
	if got := r.flag(t, lateID); !got.llm.Valid {
		t.Errorf("unread mail in another account has no model flag judgment: %+v", got)
	}
	r.syncAll(t)
	if _, err := r.d.Write.Exec(ageFailures); err != nil {
		t.Fatal(err)
	}
	r.syncAll(t)
	r.syncAll(t)
	asked := 0
	for _, s := range r.requests(t) {
		if s.caller == "flag" && s.subject == a.subject {
			asked++
		}
	}
	if asked != 2 {
		t.Errorf("the refused message was asked %d times over four passes, want 2", asked)
	}
	if got := r.flag(t, a.id); !got.present || got.llm.Valid {
		t.Errorf("the refused message flag row: %+v", got)
	}
}

func TestMailFolderStopReasonsFallBackAfterTwoCharges(t *testing.T) {
	const bad, good = "Order confirmation 55123", "Order confirmation 55124"
	for _, stop := range stopReplies {
		t.Run(stop.name, func(t *testing.T) {
			r := newRoutingRig(t, "shadow", receiptsFor(t, good), bad, good)
			record := r.api.RequestHook()
			r.api.SetOnRequest(func(req anthropictest.Request) {
				record(req)
				if body := req.JSON(t); callerOf(t, body) == folderCaller && subjectIn(body) == bad {
					r.api.PushJSON(stop.reply())
				}
			})
			first, second := r.targets[0], r.targets[1]
			r.run(t, "sync")
			if stage, _, dest := r.decision(t, second.id); stage != "llm" || dest != "Receipts" {
				t.Fatalf("the message after the %s reply routed %s/%s, want llm/Receipts", stop.name, stage, dest)
			}
			if n := r.decisions(t, first.id); n != 0 {
				t.Fatalf("the first %s reply produced %d decisions", stop.name, n)
			}
			r.run(t, "sync")
			if n := r.decisions(t, first.id); n != 0 {
				t.Fatalf("the message was decided within the retry spacing: %d decisions", n)
			}
			r.exec(t, ageFailures)
			r.run(t, "sync")
			r.run(t, "sync")
			if stage, action, dest := r.decision(t, first.id); stage != "fallback" || action != "file" || dest != "Misc" {
				t.Errorf("after two charged failures the message routed %s/%s/%s, want fallback/file/Misc", stage, action, dest)
			}
			asked := 0
			for _, body := range r.folderRequests(t) {
				if subjectIn(body) == bad {
					asked++
				}
			}
			if asked != 2 {
				t.Errorf("the message was asked %d times over four passes, want 2", asked)
			}
			if got := r.verbs(); len(got) != 0 {
				t.Fatalf("shadow routing wrote to IMAP: %v", got)
			}
		})
	}
}

func TestMailPassWithTheAnthropicConnectionDisabledRunsFreeStages(t *testing.T) {
	t.Parallel()
	r := newMailClassifierRig(t, nil)
	for _, q := range []string{"DELETE FROM mail_scans", "UPDATE connections SET enabled = 0 WHERE kind = 'service/anthropic'"} {
		if _, err := r.d.Write.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	const fresh = "Fresh mail while the assistant is off"
	h, x := mailtest.RawWith(fresh, "someone@elsewhere.test", "fresh-off", "Hello.")
	r.f.fakes["icloud"].Boxes["INBOX"].Box.Add(h, x, nil, nil)

	r.syncAll(t)
	if n := r.count(t, "SELECT COUNT(*) FROM messages WHERE account = 'icloud' AND subject = ?", fresh); n != 1 {
		t.Errorf("sync fetched %d copies of the new mail, want 1", n)
	}
	if n := r.count(t, "SELECT COUNT(*) FROM mail_scans WHERE kind = 'packages' AND found = 1"); n == 0 {
		t.Errorf("the package regex found nothing with the assistant off")
	}
	for _, m := range r.targets {
		if got := r.flag(t, m.id); !got.present || got.llm.Valid {
			t.Errorf("message %q flag row with the assistant off: %+v", m.subject, got)
		}
	}
	if code, _, stderr := r.f.Run(context.Background(), "proposals", "scan-all"); code != cli.ExitOK {
		t.Fatalf("proposals scan-all with the assistant off: exit %d stderr %q", code, stderr)
	}
	if sent := r.requests(t); len(sent) != 0 {
		t.Errorf("a disabled connection sent %d paid requests", len(sent))
	}
}

func TestMailPassRefreshStorageErrorsFailThePass(t *testing.T) {
	t.Parallel()
	r := newMailClassifierRig(t, nil)
	for _, q := range []string{
		"DELETE FROM mail_scans",
		"CREATE TRIGGER fail_scans BEFORE INSERT ON mail_scans BEGIN SELECT RAISE(ABORT, 'disk full'); END",
	} {
		if _, err := r.d.Write.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	r.recentSent(t)
	code, _, stderr := r.f.Run(context.Background(), "sync")
	if code == cli.ExitOK {
		t.Fatalf("a pass whose scan markers could not be stored succeeded: stderr %q", stderr)
	}
	for _, name := range []string{"open loops refresh", "watches refresh"} {
		if !strings.Contains(stderr, name) {
			t.Errorf("the failed pass does not name %q: %q", name, stderr)
		}
	}
	for _, m := range r.targets {
		if got := r.flag(t, m.id); !got.llm.Valid {
			t.Errorf("message %q lost its flag judgment: %+v", m.subject, got)
		}
	}
}

func TestMailPassAccountingErrorIsNotADeferral(t *testing.T) {
	t.Parallel()
	r := newMailClassifierRig(t, nil)
	a := r.targets[0]
	r.mu.Lock()
	r.hook = func(req anthropictest.Request) {
		if body := req.JSON(t); callerOf(t, body) == "flag" && subjectIn(body) == a.subject {
			r.f.api.PushJSON(anthropictest.Refusal("cyber", "declined"))
		}
	}
	r.mu.Unlock()
	if _, err := r.d.Write.Exec("CREATE TRIGGER fail_close BEFORE UPDATE OF state ON runs WHEN NEW.state = 'failed' " +
		"BEGIN SELECT RAISE(ABORT, 'disk full'); END"); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := r.f.Run(context.Background(), "sync", "--account", "icloud")
	if code == cli.ExitOK {
		t.Fatalf("a pass that could not close its accounting run succeeded: stderr %q", stderr)
	}
	if !strings.Contains(stderr, "closing the accounting run") {
		t.Errorf("stderr does not report the accounting failure: %q", stderr)
	}
	if got := r.flag(t, a.id); got.present {
		t.Errorf("the accounting failure was recorded as a judgment: %+v", got)
	}
	if n := r.count(t, "SELECT COUNT(*) FROM inference_classification_deferrals"); n != 0 {
		t.Errorf("the accounting failure was recorded as %d deferrals", n)
	}
}

func TestMailPassIgnoresARealFolderNamedUncertain(t *testing.T) {
	const subject = "Order confirmation 55123"
	r := newRoutingRig(t, "shadow", receiptsFor(t, subject), subject)
	r.fake.AddBox("uncertain", mailtest.NewBox(11))
	for i := 1; i <= 11; i++ {
		r.add("uncertain", fmt.Sprintf("Unsure %d", i), fmt.Sprintf("u%d@unsure%d.test", i, i), fmt.Sprintf("u%d", i), "Filed message.")
	}
	r.run(t, "sync")
	if n := r.count(t, "SELECT COUNT(*) FROM filed_messages WHERE folder = 'uncertain'"); n != 11 {
		t.Fatalf("the uncertain folder holds %d filed messages, want 11", n)
	}
	if stage, _, dest := r.decision(t, r.targets[0].id); stage != "llm" || dest != "Receipts" {
		t.Fatalf("decision with a folder named uncertain = %s/%s, want llm/Receipts", stage, dest)
	}
	requests := r.folderRequests(t)
	if len(requests) != 1 {
		t.Fatalf("folder requests = %d, want 1", len(requests))
	}
	format := requests[0]["output_config"].(map[string]any)["format"].(map[string]any)
	enum := format["schema"].(map[string]any)["properties"].(map[string]any)["folder"].(map[string]any)["enum"]
	if fmt.Sprint(enum) != "[Bills Receipts uncertain]" {
		t.Errorf("folder enum with a real uncertain folder = %v", enum)
	}
	if strings.Contains(userText(requests[0]), "- uncertain (") {
		t.Errorf("the real uncertain folder was offered as a destination: %q", userText(requests[0]))
	}
}

func TestRebalanceNamingKeepsPaidNamesAndFallsBackOnProviderFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const fresh = "SELECT COUNT(*) FROM proposals WHERE kind = 'rebalance_split' AND fingerprint NOT LIKE '%:old'"
	setup := func(t *testing.T) (*fixture, func(string) int, func(string)) {
		f := newFixture(t, loadGoldenData(t), nil, "")
		d := mustConnect(t, f.dir+"/atrium.db", keyFor(t))
		exec := func(query string) {
			t.Helper()
			if _, err := d.Write.ExecContext(ctx, query); err != nil {
				t.Fatal(err)
			}
		}
		count := func(query string) int {
			t.Helper()
			var n int
			if err := d.Read.QueryRowContext(ctx, query).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		exec("UPDATE proposals SET fingerprint = fingerprint || ':old' WHERE kind = 'rebalance_split'")
		return f, count, exec
	}

	t.Run("paid name kept when staging stops", func(t *testing.T) {
		t.Parallel()
		f, count, exec := setup(t)
		old := count("SELECT COUNT(*) FROM proposals WHERE kind = 'rebalance_split' AND fingerprint LIKE '%:old'")
		var mu sync.Mutex
		var prompts []string
		f.api.SetOnRequest(func(req anthropictest.Request) {
			mu.Lock()
			defer mu.Unlock()
			prompts = append(prompts, userText(req.JSON(t)))
		})
		exec("CREATE TRIGGER stop_staging BEFORE INSERT ON proposals WHEN NEW.kind = 'rebalance_split' BEGIN SELECT RAISE(ABORT, 'disk full'); END")
		if code, _, _ := f.Run(ctx, "proposals", "scan-all"); code == cli.ExitOK {
			t.Fatal("scan-all succeeded although no proposal could be staged")
		}
		mu.Lock()
		bought := len(prompts)
		mu.Unlock()
		if bought == 0 {
			t.Fatal("no folder names were asked for")
		}
		exec("DROP TRIGGER stop_staging")
		if code, _, stderr := f.Run(ctx, "proposals", "scan-all"); code != cli.ExitOK {
			t.Fatalf("scan-all: exit %d stderr %q", code, stderr)
		}
		if n := count(fresh); n != old {
			t.Fatalf("%d split proposals were staged, want %d", n, old)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(prompts) != bought {
			t.Errorf("the next scan bought %d names again: %q", len(prompts)-bought, prompts[bought:])
		}
	})

	t.Run("provider failure", func(t *testing.T) {
		t.Parallel()
		f, count, _ := setup(t)
		old := count("SELECT COUNT(*) FROM proposals WHERE kind = 'rebalance_split' AND fingerprint LIKE '%:old'")
		var mu sync.Mutex
		asked := 0
		f.api.SetOnRequest(func(anthropictest.Request) {
			mu.Lock()
			asked++
			mu.Unlock()
			f.api.PushJSON(anthropictest.Failure(http.StatusBadRequest, "invalid_request_error", "rejected"))
		})
		if code, _, stderr := f.Run(ctx, "proposals", "scan-all"); code != cli.ExitOK {
			t.Fatalf("scan-all with the provider failing: exit %d stderr %q", code, stderr)
		}
		if n := count(fresh); n != old {
			t.Fatalf("with the provider failing %d split proposals were staged, want %d with fallback names", n, old)
		}
		mu.Lock()
		defer mu.Unlock()
		if asked == 0 {
			t.Fatal("no folder names were asked for")
		}
		if n := count("SELECT COUNT(*) FROM runs r JOIN threads t ON t.id = r.thread_id WHERE t.role = 'classification:mail_folder_name' AND r.state = 'done'"); n != 0 {
			t.Errorf("%d naming runs succeeded against a failing provider", n)
		}
	})
}

func TestMailPassFinalEventOutcomesLeaveThePendingWindow(t *testing.T) {
	t.Parallel()
	r := newMailClassifierRig(t, nil)
	if _, err := r.d.Write.Exec("UPDATE messages SET seen = 0, sent = 0, in_inbox = 1, gone_at = NULL " +
		"WHERE id IN (SELECT id FROM messages WHERE account = 'icloud' ORDER BY id LIMIT 55)"); err != nil {
		t.Fatal(err)
	}
	var lateID int64
	var late string
	if err := r.d.Read.QueryRow("SELECT id, subject FROM messages WHERE id = (SELECT MAX(id) FROM "+
		"(SELECT id FROM messages WHERE account = 'icloud' ORDER BY id LIMIT 55))").Scan(&lateID, &late); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.hook = func(req anthropictest.Request) {
		if body := req.JSON(t); callerOf(t, body) == "event" && subjectIn(body) != late {
			r.f.api.PushJSON(anthropictest.Refusal("cyber", "declined"))
		}
	}
	r.mu.Unlock()

	r.syncAll(t)
	if _, err := r.d.Write.Exec(ageFailures); err != nil {
		t.Fatal(err)
	}
	r.syncAll(t)
	r.syncAll(t)
	if got := r.event(t, lateID); got == "" {
		t.Errorf("the later unread message has no event judgment behind %d refused ones", 54)
	}
	if n := r.count(t, "SELECT COUNT(*) FROM event_candidates WHERE status = 'none' AND reason = 'model gave no usable answer'"); n < 50 {
		t.Errorf("%d refused messages left the pending window, want at least 50", n)
	}
}

func (r *mailClassifierRig) calls(t *testing.T, caller string) int {
	t.Helper()
	n := 0
	for _, s := range r.requests(t) {
		if scanCaller(s.caller) == caller {
			n++
		}
	}
	return n
}

func (r *mailClassifierRig) refuseAllBut(t *testing.T, caller, keep string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hook = func(req anthropictest.Request) {
		if body := req.JSON(t); scanCaller(callerOf(t, body)) == caller && subjectIn(body) != keep {
			r.f.api.PushJSON(anthropictest.Refusal("cyber", "declined"))
		}
	}
}

func (r *mailClassifierRig) passWithWaitingItems(t *testing.T, caller string, window int, judged func() bool) {
	t.Helper()
	if code, stderr := r.sync(t); code != cli.ExitOK {
		t.Fatalf("first sync: exit %d stderr %q", code, stderr)
	}
	waiting := r.failedItems(t, "refusal")
	if len(waiting) < window || r.calls(t, caller) != len(waiting) {
		t.Fatalf("first %s pass: %d refused items for %d paid calls, want at least %d", caller, len(waiting), r.calls(t, caller), window)
	}
	if judged() {
		t.Fatalf("the later message was judged before the %s window filled", caller)
	}
	if code, stderr := r.sync(t); code != cli.ExitOK {
		t.Fatalf("second sync: exit %d stderr %q", code, stderr)
	}
	if !judged() {
		t.Errorf("the later message was not judged behind %d %s items waiting for their retry", len(waiting), caller)
	}
	failed := r.failedItems(t, "refusal")
	for key, n := range failed {
		if n != 1 {
			t.Errorf("item %s has %d charged failures within the retry spacing, want 1", key, n)
		}
	}
	for key := range waiting {
		if failed[key] == 0 {
			t.Errorf("waiting item %s lost its charged failure", key)
		}
	}
	if got, want := r.calls(t, caller), len(failed)+1; got != want {
		t.Errorf("%d paid %s calls, want %d: one per refused message and one for the later message", got, caller, want)
	}
}

func TestMailPassWaitingEventRetriesDoNotHoldTheWindow(t *testing.T) {
	t.Parallel()
	r := newMailClassifierRig(t, nil)
	const unread = 60
	if _, err := r.d.Write.Exec("UPDATE messages SET seen = 0, sent = 0, in_inbox = 1, gone_at = NULL "+
		"WHERE id IN (SELECT id FROM messages WHERE account = 'icloud' ORDER BY id LIMIT ?)", unread); err != nil {
		t.Fatal(err)
	}
	var lateID int64
	var late string
	if err := r.d.Read.QueryRow("SELECT id, subject FROM messages WHERE id = (SELECT MAX(id) FROM "+
		"(SELECT id FROM messages WHERE account = 'icloud' ORDER BY id LIMIT ?))", unread).Scan(&lateID, &late); err != nil {
		t.Fatal(err)
	}
	r.refuseAllBut(t, "event", late)
	r.passWithWaitingItems(t, "event", 50, func() bool { return r.event(t, lateID) != "" })
	r.noMailWrites(t)
}

func TestMailPassWaitingPackageRetriesDoNotHoldTheWindow(t *testing.T) {
	t.Parallel()
	r := newMailClassifierRig(t, nil)
	const shipped = 30
	for _, q := range []string{
		"DELETE FROM mail_scans",
		"UPDATE messages SET subject = 'Your package shipped ' || id, date_ts = CAST(strftime('%s', 'now') AS REAL) - 3600 - id, sent = 0, gone_at = NULL " +
			"WHERE id IN (SELECT id FROM messages WHERE account = 'icloud' ORDER BY id LIMIT " + strconv.Itoa(shipped) + ")",
	} {
		if _, err := r.d.Write.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	var lateID int64
	var late string
	if err := r.d.Read.QueryRow("SELECT id, subject FROM messages WHERE id = (SELECT MAX(id) FROM "+
		"(SELECT id FROM messages WHERE account = 'icloud' ORDER BY id LIMIT ?))", shipped).Scan(&lateID, &late); err != nil {
		t.Fatal(err)
	}
	r.refuseAllBut(t, "packages", late)
	r.passWithWaitingItems(t, "packages", 20, func() bool {
		return r.count(t, "SELECT COUNT(*) FROM mail_scans WHERE kind = 'packages' AND message_id = ?", lateID) == 1
	})
	r.noMailWrites(t)
}

func TestMailWatchPicksUpAReenabledAnthropicConnectionAtTheNextPass(t *testing.T) {
	t.Parallel()
	r := newMailClassifierRig(t, nil)
	if _, err := r.d.Write.Exec("UPDATE connections SET enabled = 0 WHERE kind = 'service/anthropic'"); err != nil {
		t.Fatal(err)
	}
	const fresh = "Fresh mail before the assistant is back"
	h, x := mailtest.RawWith(fresh, "someone@elsewhere.test", "fresh-back", "Hello.")
	r.f.fakes["icloud"].Boxes["INBOX"].Box.Add(h, x, nil, nil)
	r.f.fakes["icloud"].Idle = true

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan int, 1)
	go func() {
		code, _, _ := r.f.Run(ctx, "watch", "--account", "icloud")
		done <- code
	}()
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for !ok() {
			if ctx.Err() != nil {
				t.Fatalf("watch never %s", what)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	waitFor("synced the new mail", func() bool {
		return r.count(t, "SELECT COUNT(*) FROM messages WHERE account = 'icloud' AND subject = ?", fresh) == 1
	})
	if sent := r.requests(t); len(sent) != 0 {
		t.Fatalf("a disabled connection sent %d paid requests", len(sent))
	}
	if _, err := r.d.Write.Exec("UPDATE connections SET enabled = 1 WHERE kind = 'service/anthropic'"); err != nil {
		t.Fatal(err)
	}
	waitFor("asked a paid classification after the connection was re-enabled", func() bool { return len(r.requests(t)) > 0 })
	cancel()
	<-done
}

func TestMailPassWithAnUnbuildableAnthropicTransportRunsFreeStages(t *testing.T) {
	t.Parallel()
	r := newMailClassifierRig(t, nil)
	hostPath, _ := r.f.lookup("ATRIUM_HOST_CONFIG")
	setEnv(t, "ATRIUM_HOST_CONFIG", hostConfigFrom(t, hostPath, r.f.dir, "host-broken-ca.toml", map[string]string{
		`kind = "service/anthropic"`: `kind = "service/anthropic"` + "\ntransport = { ca_file = " + strconv.Quote(filepath.Join(r.f.dir, "missing-ca.pem")) + " }",
	}))
	const fresh = "Fresh mail while the transport is broken"
	h, x := mailtest.RawWith(fresh, "someone@elsewhere.test", "fresh-transport", "Hello.")
	r.f.fakes["icloud"].Boxes["INBOX"].Box.Add(h, x, nil, nil)

	code, _, stderr := r.f.runLogged(context.Background(), "sync", "--account", "icloud")
	if code != cli.ExitOK {
		t.Fatalf("sync with an unbuildable transport: exit %d stderr %q", code, stderr)
	}
	if n := strings.Count(stderr, "paid classification waits: the Anthropic connection transport cannot be built"); n != 1 {
		t.Errorf("%d transport warnings, want 1: %q", n, stderr)
	}
	deferral := ""
	for line := range strings.SplitSeq(stderr, "\n") {
		if strings.Contains(line, "paid classification deferred for the rest of this pass") {
			deferral = line
		}
	}
	if !strings.Contains(deferral, "CA bundle") || strings.Contains(deferral, "is not configured") {
		t.Errorf("the pass deferral log does not name the transport cause: %q", deferral)
	}
	if n := r.count(t, "SELECT COUNT(*) FROM messages WHERE account = 'icloud' AND subject = ?", fresh); n != 1 {
		t.Errorf("sync fetched %d copies of the new mail, want 1", n)
	}
	for _, m := range r.targets {
		if got := r.flag(t, m.id); !got.present || got.llm.Valid {
			t.Errorf("message %q flag row with an unbuildable transport: %+v", m.subject, got)
		}
	}
	if sent := r.requests(t); len(sent) != 0 {
		t.Errorf("an unbuildable transport sent %d paid requests", len(sent))
	}
}
