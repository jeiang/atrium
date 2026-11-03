package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

var attentionNow = time.Date(2030, 6, 10, 2, 0, 0, 0, time.UTC)

func attentionList(t *testing.T, at time.Time) (attention.Result, string) {
	t.Helper()
	ran := runApp("attention", "list", "--json", "--at", at.Format(time.RFC3339))
	if ran.code != cli.ExitOK {
		t.Fatalf("attention list: exit %d stderr %q", ran.code, ran.stderr)
	}
	var res attention.Result
	if err := json.Unmarshal([]byte(ran.stdout), &res); err != nil {
		t.Fatalf("attention list output %q: %v", ran.stdout, err)
	}
	return res, ran.stdout
}

func attentionKeys(items []attention.Item) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.Kind+"/"+it.SourceID)
	}
	return out
}

func attentionSeed(t *testing.T, dir string) {
	t.Helper()
	if ran := runApp("attention", "list"); ran.code != cli.ExitOK {
		t.Fatalf("creating the schema: exit %d stderr %q", ran.code, ran.stderr)
	}
	if err := os.MkdirAll(filepath.Join(dir, "media-contacts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "media-contacts", "ada.vcf"), []byte("BEGIN:VCARD\nVERSION:3.0\nFN:Ada Lovelace\nBDAY:--0615\nEND:VCARD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	ago := func(days int) float64 { return float64(attentionNow.AddDate(0, 0, -days).Unix()) }
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO proposals (id, kind, status, created_ts, title, fingerprint) VALUES (1, 'rebalance', 'pending', ?, 'Move receipts', 'f1')", []any{ago(7)}},
		{"INSERT INTO proposals (id, kind, status, created_ts, title, fingerprint) VALUES (3, 'inbox_backlog', 'pending', ?, 'Sort backlog', 'f3')", []any{ago(7)}},
		{"INSERT INTO proposals (id, kind, status, created_ts, title, fingerprint) VALUES (2, 'rebalance', 'applied', ?, 'Old move', 'f2')", []any{ago(8)}},
		{"INSERT INTO open_loops (thread_key, account, state, last_message_id, since, counterpart, subject, updated_ts) VALUES ('t1', 'icloud', 'owe_reply', 1, ?, 'Bo', 'Lunch', 1)", []any{ago(6)}},
		{"INSERT INTO open_loops (thread_key, account, state, last_message_id, since, counterpart, subject, updated_ts) VALUES ('t2', 'icloud', 'waiting_on_them', 2, ?, 'Cy', 'Quote', 1)", []any{ago(10)}},
		{"INSERT INTO open_loops (thread_key, account, state, last_message_id, since, counterpart, subject, snoozed_until, updated_ts) VALUES ('t3', 'icloud', 'owe_reply', 3, ?, 'Di', 'Hidden by loop snooze', ?, 1)", []any{ago(20), float64(attentionNow.AddDate(0, 0, 5).Unix())}},
		{"INSERT INTO open_loops (thread_key, account, state, last_message_id, since, counterpart, subject, dismissed, updated_ts) VALUES ('t4', 'icloud', 'owe_reply', 4, ?, 'Ed', 'Dismissed loop', 1, 1)", []any{ago(20)}},
		{"INSERT INTO drafts (id, account, to_addrs, subject, body, status, created_at, updated_at) VALUES (1, 'icloud', 'bo@example.com', 'Re: Lunch', 'x', 'draft', ?, ?)", []any{ago(5), ago(5)}},
		{"INSERT INTO drafts (id, account, to_addrs, subject, body, status, created_at, updated_at) VALUES (2, 'icloud', 'bo@example.com', 'Sent already', 'x', 'sent', ?, ?)", []any{ago(5), ago(5)}},
		{"INSERT INTO event_candidates (message_id, reason, status, ts) VALUES (7, 'dinner on friday', 'queued', ?)", []any{ago(4)}},
		{"INSERT INTO event_candidates (message_id, reason, status, ts) VALUES (8, 'handled', 'created', ?)", []any{ago(4)}},
		{"INSERT INTO commitments (id, message_id, text, owner, due, created_ts) VALUES (1, 1, 'Send the report', 'me', '2030-06-08', ?)", []any{ago(1)}},
		{"INSERT INTO commitments (id, message_id, text, owner, due, created_ts) VALUES (2, 2, 'Call the bank', 'me', '2030-06-09', ?)", []any{ago(8)}},
		{"INSERT INTO commitments (id, message_id, text, owner, due, created_ts) VALUES (3, 3, 'Book the venue', 'me', '2030-06-12', ?)", []any{ago(9)}},
		{"INSERT INTO commitments (id, message_id, text, owner, created_ts) VALUES (4, 4, 'Reply to the landlord', 'me', ?)", []any{ago(3)}},
		{"INSERT INTO commitments (id, message_id, text, owner, due, created_ts) VALUES (5, 5, 'Renew the passport', 'me', '2030-06-20', ?)", []any{ago(2)}},
		{"INSERT INTO commitments (id, message_id, text, owner, due, created_ts) VALUES (6, 6, 'They will send the file', 'them', '2030-06-01', ?)", []any{ago(2)}},
		{"INSERT INTO commitments (id, message_id, text, owner, status, created_ts) VALUES (7, 7, 'Finished item', 'me', 'done', ?)", []any{ago(2)}},
		{"INSERT INTO threads (id, kind, title, effort, created_at, updated_at) VALUES (1, 'chat', 'Trip', 'medium', ?, ?)", []any{ago(2), ago(1)}},
		{"INSERT INTO thread_plans (thread_id, items_json, updated_at) VALUES (1, '[{\"text\":\"Book flights\",\"done\":true},{\"text\":\"Pick hotel\",\"done\":false}]', ?)", []any{ago(1)}},
		{"INSERT INTO packages (id, carrier, tracking_number, merchant, status, status_text, source_message_id, last_message_id, updated_ts, first_seen) VALUES (1, 'ups', '1Z', 'Acme', 'in_transit', 'In transit', 1, 1, ?, ?)", []any{ago(1), ago(3)}},
	} {
		if _, err := d.Write.Exec(stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.sql, err)
		}
	}
}

func TestAttentionQueueIsOrderedStableAndGrouped(t *testing.T) {
	dir, _ := moduleFixture(t, flagsExcept(module.Mail, "true"))
	attentionSeed(t, dir)

	res, first := attentionList(t, attentionNow)
	_, second := attentionList(t, attentionNow)
	if first != second {
		t.Errorf("two reads differ:\n%s\n%s", first, second)
	}
	want := []string{
		"commitment/1", "commitment/3", "commitment/2",
		"proposal/1", "proposal/3", "owe_reply/1", "draft/1", "event_candidate/7", "commitment/4", "commitment/5",
		"thread_plan/1",
	}
	if got := attentionKeys(res.Items); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("items %v, want %v", got, want)
	}
	classes := map[string]string{}
	for _, it := range res.Items {
		classes[it.Kind+"/"+it.SourceID] = it.Class
	}
	for key, class := range map[string]string{"commitment/1": attention.Overdue, "commitment/2": attention.DueSoon, "commitment/3": attention.DueSoon, "commitment/5": attention.NeedsDecision} {
		if classes[key] != class {
			t.Errorf("%s is %q, want %q (day boundaries follow the configured zone, not UTC)", key, classes[key], class)
		}
	}
	wantGroups := []attention.Group{{Name: "commitment", Count: 5}, {Name: "draft", Count: 1}, {Name: "event_candidate", Count: 1}, {Name: "owe_reply", Count: 1}, {Name: "proposal", Count: 2}, {Name: "thread_plan", Count: 1}}
	if fmt.Sprint(res.Groups) != fmt.Sprint(wantGroups) {
		t.Errorf("groups %v, want %v", res.Groups, wantGroups)
	}
	if got := attentionKeys(res.Waiting); strings.Join(got, " ") != "waiting_on_them/2" {
		t.Errorf("waiting %v", got)
	}
	if got := attentionKeys(res.Upcoming); len(got) != 2 || got[0] != "package/1" || !strings.HasPrefix(got[1], "birthday/") {
		t.Errorf("upcoming %v", got)
	}
	for _, it := range append(append([]attention.Item{}, res.Waiting...), res.Upcoming...) {
		if len(it.Actions) != 0 {
			t.Errorf("%s/%s offers actions %v", it.Kind, it.SourceID, it.Actions)
		}
	}
	for _, it := range res.Items {
		switch it.Kind {
		case attention.KindProposal:
			approve := it.SourceID == "1"
			if approve && (len(it.Actions) != 2 || it.Actions[0].Path != "/proposals/1/approve" || it.Actions[0].Method != "POST") {
				t.Errorf("proposal actions %v", it.Actions)
			}
			if !approve && (len(it.Actions) != 1 || !strings.HasSuffix(it.Actions[0].Path, "/dismiss")) {
				t.Errorf("proposal %s needs a message selection, so it offers only %v", it.SourceID, it.Actions)
			}
		case attention.KindEvent, attention.KindThreadPlan:
			if len(it.Actions) != 0 {
				t.Errorf("%s offers actions %v", it.Kind, it.Actions)
			}
		}
	}
	text := runApp("attention", "list", "--at", attentionNow.Format(time.RFC3339))
	if text.code != cli.ExitOK || !strings.Contains(text.stdout, "overdue\tcommitment/1\tSend the report\tdue 2030-06-08") || !strings.Contains(text.stdout, "Waiting\n") {
		t.Errorf("text output: exit %d %q", text.code, text.stdout)
	}
}

func TestAttentionQueueFollowsModuleToggles(t *testing.T) {
	mailKinds := []string{attention.KindDraft, attention.KindOweReply, attention.KindCommitment, attention.KindWaiting, attention.KindPackage, attention.KindBirthday}
	for _, tc := range []struct {
		off     string
		removed []string
	}{
		{module.Mail, mailKinds},
		{module.Calendar, []string{attention.KindEvent}},
		{module.Assistant, []string{attention.KindThreadPlan}},
		{module.Contacts, []string{attention.KindBirthday}},
	} {
		t.Run(tc.off, func(t *testing.T) {
			dir, _ := moduleFixture(t, flagsExcept(tc.off, "true"))
			attentionSeed(t, dir)
			total := func(r attention.Result) int { return len(r.Items) + len(r.Waiting) + len(r.Upcoming) }
			all, _ := attentionList(t, attentionNow)
			for _, flag := range []string{"false", "true"} {
				t.Setenv(flagEnv(tc.off), flag)
				res, _ := attentionList(t, attentionNow)
				seen := map[string]bool{}
				for _, it := range append(append(append([]attention.Item{}, res.Items...), res.Waiting...), res.Upcoming...) {
					seen[it.Kind] = true
				}
				for _, kind := range tc.removed {
					if seen[kind] == (flag == "false") {
						t.Errorf("%s=%s: kind %s present = %v", tc.off, flag, kind, seen[kind])
					}
				}
				if flag == "false" && total(res) == total(all) {
					t.Errorf("disabling %s removed nothing from the queue", tc.off)
				}
				if !seen[attention.KindProposal] {
					t.Errorf("core proposals vanished with %s=%s", tc.off, flag)
				}
			}
		})
	}
}

func TestAttentionSnoozeHidesAnItemUntilItsDate(t *testing.T) {
	dir, _ := moduleFixture(t, flagsExcept(module.Mail, "true"))
	attentionSeed(t, dir)
	has := func(at time.Time, key string) bool {
		res, _ := attentionList(t, at)
		for _, group := range [][]attention.Item{res.Items, res.Waiting, res.Upcoming} {
			for _, k := range attentionKeys(group) {
				if k == key {
					return true
				}
			}
		}
		return false
	}
	for _, key := range []string{"owe_reply/1", "waiting_on_them/2"} {
		kind, id, _ := strings.Cut(key, "/")
		if got := runApp("attention", "snooze", kind, id, "--until", "2030-06-12"); got.code != cli.ExitOK {
			t.Fatalf("snooze %s: exit %d %q", key, got.code, got.stderr)
		}
		localMidnight := time.Date(2030, 6, 12, 4, 0, 0, 0, time.UTC)
		if has(attentionNow, key) || has(localMidnight.Add(-time.Second), key) {
			t.Errorf("%s shows before the snooze date", key)
		}
		if !has(localMidnight, key) {
			t.Errorf("%s still hidden at local midnight of the snooze date", key)
		}
	}
	for _, args := range [][]string{
		{"attention", "snooze", "nonsense", "1", "--until", "2030-06-12"},
		{"attention", "snooze", "owe_reply", "1", "--until", "soon"},
		{"attention", "list", "--at", "yesterday"},
	} {
		if got := runApp(args...); got.code != cli.ExitUsage {
			t.Errorf("%v: exit %d, want usage error", args, got.code)
		}
	}
}

func TestAttentionMigratesAGoldenDatabaseCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	copyGoldenFile(t, filepath.Join(golden.Dir(), "atrium.db"), path)
	t.Setenv("ATRIUM_DB", path)
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_CORE_TZ", "America/Port_of_Spain")

	count := func(d *db.DB, table string) int {
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	raw, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]int{}
	for _, table := range []string{"threads", "messages", "proposals", "usage"} {
		before[table] = count(raw, table)
	}
	_ = raw.Close()

	var out, errs bytes.Buffer
	if code := newApp().Run(context.Background(), []string{"attention", "snooze", "proposal", "1", "--until", "2030-01-01"}, &out, &errs); code != cli.ExitOK {
		t.Fatalf("snooze on a golden copy: %d %s", code, errs.String())
	}
	if res, _ := attentionList(t, attentionNow); res.Items == nil {
		t.Fatal("attention list on a golden copy returned no item list")
	}
	migrated, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrated.Close() }()
	for table, n := range before {
		if got := count(migrated, table); got != n {
			t.Errorf("%s rows changed: %d -> %d", table, n, got)
		}
	}
	var applied, snoozes int
	if err := migrated.Read.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = 17").Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("migration 17 applied %d times: %v", applied, err)
	}
	if snoozes = count(migrated, "attention_snoozes"); snoozes != 1 {
		t.Fatalf("snoozes %d", snoozes)
	}
}
