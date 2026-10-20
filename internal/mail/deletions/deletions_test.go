package deletions_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/deletions"
	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/mail/rulestest"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
)

const day = 86400

func scan(t *testing.T, d *db.DB) int {
	t.Helper()
	r, err := deletions.Scan(context.Background(), d, 1_792_080_000)
	if err != nil {
		t.Fatal(err)
	}
	return r.Created
}

func group(t *testing.T, d *db.DB, uid int, addr, tmpl string, n int, fields rulestest.Fields) []int64 {
	t.Helper()
	ids := make([]int64, n)
	for i := range n {
		row := rulestest.Fields{"from_addr": addr, "from_domain": strings.SplitN(addr, "@", 2)[1], "subject_tmpl": tmpl, "subject": tmpl, "seen": 1, "date_ts": 100.0}
		for k, v := range fields {
			row[k] = v
		}
		ids[i] = rulestest.Add(t, d, uid+i, nil, "", row)
	}
	return ids
}

func deleteEvents(t *testing.T, d *db.DB, ids []int64) {
	t.Helper()
	for _, id := range ids {
		rulestest.Exec(t, d, "UPDATE messages SET gone_at = 1 WHERE id = ?", id)
		rulestest.Exec(t, d, "INSERT INTO events (ts, account, kind, message_id) VALUES (1, 'icloud', 'delete', ?)", id)
	}
}

func list(t *testing.T, d *db.DB, status string) []*proposals.Proposal {
	t.Helper()
	out, err := proposals.List(context.Background(), d.Read, status, deletions.Kind)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func get(v any, key string) any {
	d, _ := v.(py.Dict)
	out, _ := d.Get(key)
	return out
}

func TestDeletionThresholdAndBacklog(t *testing.T) {
	d := rulestest.DB(t)
	hit := group(t, d, 1, "n@news.test", "weekly", 6, nil)
	deleteEvents(t, d, hit[:5])
	near := group(t, d, 20, "m@mix.test", "daily", 10, nil)
	deleteEvents(t, d, near[:6])
	few := group(t, d, 40, "f@few.test", "rare", 4, nil)
	deleteEvents(t, d, few)
	if n := scan(t, d); n != 1 {
		t.Fatalf("created = %d", n)
	}
	pending := list(t, d, proposals.Pending)
	if len(pending) != 1 {
		t.Fatalf("pending = %v", pending)
	}
	p := pending[0]
	when := get(get(p.Preview, "rule"), "when").([]any)
	if get(p.Preview, "backlog") != int64(1) || py.JSONDumps(when[0]) != `{"field": "from_addr", "op": "eq", "value": "n@news.test"}` {
		t.Fatalf("preview = %v", p.Preview)
	}
	if p.Title != "Delete 1 messages and future mail matching n@news.test / weekly" {
		t.Fatalf("title = %q", p.Title)
	}
	full, err := proposals.Get(context.Background(), d.Read, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := py.JSONLoads(full.Payload)
	rule := get(payload, "rule")
	if get(rule, "action") != "delete" || len(get(payload, "messages").([]any)) != 1 {
		t.Fatalf("payload = %v", payload)
	}
	if _, err := rules.ValidateRule(rule); err != nil {
		t.Fatal(err)
	}
	if n := scan(t, d); n != 0 {
		t.Fatalf("rescan created = %d", n)
	}
	if err := proposals.Dismiss(context.Background(), d.Write, p.ID, true, 1); err != nil {
		t.Fatal(err)
	}
	if n := scan(t, d); n != 0 {
		t.Fatalf("created after suppress = %d", n)
	}
	if all := list(t, d, ""); len(all) != 1 {
		t.Fatalf("proposals = %v", all)
	}
}

func TestOTPTemplatesProposeAgeLimitedRule(t *testing.T) {
	d := rulestest.DB(t)
	const now = 1_792_080_000.0
	codes := group(t, d, 1, "no@otp.test", "your code is <n>", 6, rulestest.Fields{"k_otp6": 1})
	for _, id := range codes[:5] {
		rulestest.Exec(t, d, "UPDATE messages SET date_ts = ? WHERE id = ?", now-10*day, id)
	}
	rulestest.Exec(t, d, "UPDATE messages SET date_ts = ? WHERE id = ?", now, codes[5])
	if n := scan(t, d); n != 1 {
		t.Fatalf("created = %d", n)
	}
	all := list(t, d, "")
	if len(all) != 1 {
		t.Fatalf("proposals = %v", all)
	}
	p := all[0].Preview
	if get(p, "reason") != "otp" || get(p, "older_than_days") != int64(7) || get(p, "backlog") != int64(5) {
		t.Fatalf("preview = %v", p)
	}
	found := false
	for _, c := range get(get(p, "rule"), "when").([]any) {
		found = found || py.JSONDumps(c) == `{"field": "age_days", "op": "gte", "value": 7}`
	}
	if !found {
		t.Fatalf("rule = %v", get(p, "rule"))
	}
}

func TestExistingRuleSuppressesDeletionProposal(t *testing.T) {
	d := rulestest.DB(t)
	hit := group(t, d, 1, "n@news.test", "weekly", 6, nil)
	deleteEvents(t, d, hit[:5])
	rulestest.Exec(t, d, "INSERT INTO rules (position, id, account, action, dest, conditions) VALUES (0, 'mine', NULL, 'delete', NULL, ?)",
		`[{"field": "from_addr", "op": "eq", "value": "n@news.test"}]`)
	if n := scan(t, d); n != 0 {
		t.Fatalf("created = %d", n)
	}
}

func TestBuildRule(t *testing.T) {
	got := py.JSONDumps(deletions.BuildRule("gmail", "domain", "login.example", "your login code <n>", 7))
	want := `{"id": "del-ee72bd9f0f", "account": "gmail", "action": "delete", "when": [{"field": "from_domain", "op": "suffix", "value": "login.example"}, ` +
		`{"field": "subject_tmpl", "op": "eq", "value": "your login code <n>"}, {"field": "age_days", "op": "gte", "value": 7}]}`
	if got != want {
		t.Fatalf("BuildRule = %s", got)
	}
}
