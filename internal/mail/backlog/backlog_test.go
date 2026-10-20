package backlog_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail/backlog"
	"github.com/jeiang/atrium/internal/mail/corrections"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/live/livetest"
	"github.com/jeiang/atrium/internal/py"
)

const (
	seen    = livetest.Seen
	flagged = livetest.Flagged
)

type proposal struct {
	Kind, Account, Status, Title, Payload, Preview, Fingerprint string
	CreatedTS                                                   float64
}

func get(t *testing.T, w *livetest.World, id int64) proposal {
	t.Helper()
	var p proposal
	if err := w.DB.Read.QueryRow("SELECT kind, account, status, title, payload, preview, fingerprint, created_ts FROM proposals WHERE id = ?", id).
		Scan(&p.Kind, &p.Account, &p.Status, &p.Title, &p.Payload, &p.Preview, &p.Fingerprint, &p.CreatedTS); err != nil {
		t.Fatal(err)
	}
	return p
}

func groups(t *testing.T, p proposal, field string) []any {
	t.Helper()
	raw := p.Payload
	if field == "preview" {
		raw = p.Preview
	}
	v, err := py.JSONLoads([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	g, _ := v.(py.Dict).Get("groups")
	return g.([]any)
}

func field(v any, key string) any {
	out, _ := v.(py.Dict).Get(key)
	return out
}

func messageIDs(group any) []int64 {
	var out []int64
	for _, m := range field(group, "messages").([]any) {
		out = append(out, field(m, "id").(int64))
	}
	return out
}

func TestBacklogGroupsEligibleBaselineMailPerAccount(t *testing.T) {
	w := livetest.ICloud(t, "live",
		livetest.Seed{Subject: "a1", Flags: []string{seen}}, livetest.Seed{Subject: "a2", Flags: []string{seen}},
		livetest.Seed{Subject: "b1", Flags: []string{seen}}, livetest.Seed{Subject: "unread"},
		livetest.Seed{Subject: "flagged", Flags: []string{seen, flagged}})
	for subject, dest := range map[string]string{"a1": "Bills", "a2": "Bills", "b1": "Travel", "unread": "Bills", "flagged": "Bills"} {
		w.Decide(subject, "file", new(dest), "learned")
	}
	w.Arrive("after")
	w.Decide("after", "file", new("Bills"), "learned")
	out, err := backlog.Backlog(t.Context(), w.DB, 1_792_100_000)
	if err != nil || len(out.Created) != 1 {
		t.Fatalf("Backlog = %+v, %v", out, err)
	}
	pid := out.Created[0]
	if got := py.JSONDumps(out.Accounts); got != `{"icloud": `+itoa(pid)+`, "gmail": null}` {
		t.Fatalf("accounts = %s", got)
	}
	again, err := backlog.Backlog(t.Context(), w.DB, 1_792_100_001)
	if err != nil || len(again.Created) != 0 || py.JSONDumps(again.Accounts) != `{"icloud": null, "gmail": null}` {
		t.Fatalf("second Backlog = %+v, %v", again, err)
	}
	p := get(t, w, pid)
	golive, _ := live.GetState(t.Context(), w.DB.Read, live.GoliveKey)
	if p.Kind != backlog.Kind || p.Account != config.ICloud || p.Status != "pending" || p.CreatedTS != 1_792_100_000 ||
		p.Title != "File 3 read inbox messages into 2 folders" || !strings.HasPrefix(p.Fingerprint, "inbox_backlog:icloud:") ||
		*golive != "1792080000.0" {
		t.Fatalf("proposal = %+v", p)
	}
	payload := groups(t, p, "payload")
	if len(payload) != 2 || field(payload[0], "dest") != "Bills" || field(payload[1], "dest") != "Travel" ||
		!reflect.DeepEqual(messageIDs(payload[0]), []int64{w.MID("a1"), w.MID("a2")}) {
		t.Fatalf("payload groups = %v", py.JSONDumps(payload))
	}
	first := field(payload[0], "messages").([]any)[0]
	if keys := keysOf(first); !reflect.DeepEqual(keys, []string{"id", "uid", "uidvalidity", "msgid", "digest", "mailbox", "imap_name"}) {
		t.Fatalf("snapshot keys = %v", keys)
	}
	if !strings.HasPrefix(p.Payload, `{"folder": "INBOX", "groups": [{"dest": "Bills", "messages": [{"id": `) {
		t.Fatalf("payload = %s", p.Payload)
	}
	preview := groups(t, p, "preview")
	want := `{"name": "Bills", "count": 2, "terms": [], "senders": [{"label": "a@x.test", "count": 2}], "subjects": ["a1", "a2"], ` +
		`"messages": [{"id": 1, "subject": "a1", "from_name": "", "from_addr": "a@x.test", "date_ts": 1791280800.0}, ` +
		`{"id": 2, "subject": "a2", "from_name": "", "from_addr": "a@x.test", "date_ts": 1791280800.0}]}`
	if got := py.JSONDumps(preview[0]); got != want {
		t.Fatalf("preview group = %s", got)
	}
}

func keysOf(v any) []string {
	var out []string
	for _, p := range v.(py.Dict) {
		out = append(out, p.Key)
	}
	return out
}

func itoa(n int64) string { return py.JSONDumps(n) }

func TestGmailBacklogKeepsUncertainOut(t *testing.T) {
	w := livetest.Gmail(t, "live",
		livetest.Seed{Subject: "g1", Flags: []string{seen}, Labels: []string{`\Inbox`}},
		livetest.Seed{Subject: "g2", Flags: []string{seen}, Labels: []string{`\Inbox`}},
		livetest.Seed{Subject: "g3", Flags: []string{seen}, Labels: []string{`\Inbox`}})
	w.Decide("g1", "file", new("Receipts"), "learned")
	w.Decide("g2", "file", new("Receipts"), "learned")
	w.Decide("g3", "none", nil, "fallback")
	out, err := backlog.Backlog(t.Context(), w.DB, 1)
	if err != nil || len(out.Created) != 1 {
		t.Fatalf("Backlog = %+v, %v", out, err)
	}
	p := get(t, w, out.Created[0])
	g := groups(t, p, "payload")
	if !strings.HasPrefix(p.Payload, `{"folder": "\\Inbox", `) || len(g) != 1 || len(messageIDs(g[0])) != 2 ||
		p.Title != "File 2 read inbox messages into 1 labels" {
		t.Fatalf("gmail proposal = %+v", p)
	}
}

func TestBacklogRequiresGoLiveBaseline(t *testing.T) {
	w := livetest.ICloud(t, "live")
	w.Exec("DELETE FROM live_state")
	_, err := backlog.Backlog(t.Context(), w.DB, 1)
	var e backlog.Error
	if !errors.As(err, &e) || py.ExceptionText(err) != "BacklogError: the live service has not run yet: no go-live baseline exists" {
		t.Fatalf("Backlog before go-live = %v", err)
	}
}

func correctBeforeGoLive(t *testing.T, w *livetest.World, subject string) int64 {
	t.Helper()
	mid := w.MID(subject)
	if _, err := corrections.Record(t.Context(), w.DB, mid, "Bills", corrections.Source, w.Clock); err != nil {
		t.Fatal(err)
	}
	w.Exec("UPDATE corrections SET ts = 1.0 WHERE message_id = ?", mid)
	return mid
}

func TestShadowEraCorrectionsEnterTheBacklogAndNewOnesDoNot(t *testing.T) {
	w := livetest.ICloud(t, "live",
		livetest.Seed{Subject: "read", Flags: []string{seen}}, livetest.Seed{Subject: "unread"},
		livetest.Seed{Subject: "starred", Flags: []string{seen, flagged}}, livetest.Seed{Subject: "late", Flags: []string{seen}})
	for _, s := range []string{"read", "unread", "starred"} {
		correctBeforeGoLive(t, w, s)
	}
	if _, err := corrections.Record(t.Context(), w.DB, w.MID("late"), "Bills", corrections.Source, w.Clock); err != nil {
		t.Fatal(err)
	}
	out, err := backlog.Backlog(t.Context(), w.DB, 1)
	if err != nil || len(out.Created) != 1 {
		t.Fatalf("Backlog = %+v, %v", out, err)
	}
	g := groups(t, get(t, w, out.Created[0]), "payload")
	if len(g) != 1 || field(g[0], "dest") != "Bills" || !reflect.DeepEqual(messageIDs(g[0]), []int64{w.MID("read")}) {
		t.Fatalf("groups = %s", py.JSONDumps(g))
	}
	if len(w.Fake.Writes) != 0 {
		t.Fatalf("backlog wrote %v", w.Fake.Writes)
	}
}

func TestBlockedMailStaysOutOfTheBacklog(t *testing.T) {
	w := livetest.ICloud(t, "live", livetest.Seed{Subject: "a", Flags: []string{seen}}, livetest.Seed{Subject: "b", Flags: []string{seen}})
	w.Decide("a", "file", new("Bills"), "learned")
	w.Decide("b", "file", new("Bills"), "learned")
	var digest string
	_ = w.DB.Read.QueryRow("SELECT digest FROM messages WHERE id = ?", w.MID("a")).Scan(&digest)
	if _, _, err := w.Live.Begin(t.Context(), config.ICloud, w.MID("a"), digest, live.File, nil, new("Bills"), py.Dict{}, nil); err != nil {
		t.Fatal(err)
	}
	out, _ := backlog.Backlog(t.Context(), w.DB, 1)
	if g := groups(t, get(t, w, out.Created[0]), "payload"); !reflect.DeepEqual(messageIDs(g[0]), []int64{w.MID("b")}) {
		t.Fatalf("groups = %s", py.JSONDumps(g))
	}
}

func TestRefreshDismissesAndRebuildsAfterRebaseline(t *testing.T) {
	w := livetest.ICloud(t, "live", livetest.Seed{Subject: "old", Flags: []string{seen}})
	w.Decide("old", "file", new("Bills"), "learned")
	first, err := backlog.Backlog(t.Context(), w.DB, 1)
	if err != nil || len(first.Created) != 1 {
		t.Fatalf("Backlog = %+v, %v", first, err)
	}
	w.Arrive("again")
	w.Decide("again", "file", new("Bills"), "learned")
	w.Clock += 100
	if n, err := w.Live.Rebaseline(t.Context()); err != nil || n != 2 {
		t.Fatalf("Rebaseline = %d, %v", n, err)
	}
	second, err := backlog.Refresh(t.Context(), w.DB, 2)
	if err != nil || len(second.Created) != 1 || second.Created[0] == first.Created[0] {
		t.Fatalf("Refresh = %+v, %v", second, err)
	}
	if p := get(t, w, first.Created[0]); p.Status != "dismissed" {
		t.Fatalf("first proposal = %s", p.Status)
	}
	g := groups(t, get(t, w, second.Created[0]), "payload")
	if !reflect.DeepEqual(messageIDs(g[0]), []int64{w.MID("old"), w.MID("again")}) {
		t.Fatalf("groups = %s", py.JSONDumps(g))
	}
}

func TestFailedBacklogProposalIsReopened(t *testing.T) {
	w := livetest.ICloud(t, "live", livetest.Seed{Subject: "old", Flags: []string{seen}})
	w.Decide("old", "file", new("Bills"), "learned")
	first, _ := backlog.Backlog(t.Context(), w.DB, 1)
	w.Exec("UPDATE proposals SET status = 'failed', result = '{}' WHERE id = ?", first.Created[0])
	again, err := backlog.Backlog(t.Context(), w.DB, 5)
	if err != nil || !reflect.DeepEqual(again.Created, first.Created) {
		t.Fatalf("Backlog after failure = %+v, %v", again, err)
	}
	if p := get(t, w, first.Created[0]); p.Status != "pending" || p.CreatedTS != 5 {
		t.Fatalf("reopened = %+v", p)
	}
}

func TestBacklogMatchesPython(t *testing.T) {
	w := livetest.ICloud(t, "live",
		livetest.Seed{Subject: "a1", Flags: []string{seen}}, livetest.Seed{Subject: "a2", Flags: []string{seen}},
		livetest.Seed{Subject: "b1", Flags: []string{seen}}, livetest.Seed{Subject: "unread"},
		livetest.Seed{Subject: "flagged", Flags: []string{seen, flagged}})
	for subject, dest := range map[string]string{"a1": "Bills", "a2": "Bills", "b1": "Travel", "unread": "Bills", "flagged": "Bills"} {
		w.Decide(subject, "file", new(dest), "learned")
	}
	out, err := backlog.Backlog(t.Context(), w.DB, 1)
	if err != nil || len(out.Created) != 1 {
		t.Fatalf("Backlog = %+v, %v", out, err)
	}
	p := get(t, w, out.Created[0])
	want := proposal{Kind: backlog.Kind, Account: config.ICloud, Status: "pending", CreatedTS: 1,
		Title:       "File 3 read inbox messages into 2 folders",
		Fingerprint: "inbox_backlog:icloud:a7e665967e683463",
		Payload:     "{\"folder\": \"INBOX\", \"groups\": [{\"dest\": \"Bills\", \"messages\": [{\"id\": 1, \"uid\": 1, \"uidvalidity\": 1, \"msgid\": \"a1@x.test\", \"digest\": \"fccfbcea6500cb02d8e4d80991c250a00d9011fa1c68dbaf04854c22be50fb1a\", \"mailbox\": \"INBOX\", \"imap_name\": \"INBOX\"}, {\"id\": 2, \"uid\": 2, \"uidvalidity\": 1, \"msgid\": \"a2@x.test\", \"digest\": \"7b464f614cf3b4cbc283b6e93d8062e5697251d25c6da7f3512ef6aa3cd3e3eb\", \"mailbox\": \"INBOX\", \"imap_name\": \"INBOX\"}]}, {\"dest\": \"Travel\", \"messages\": [{\"id\": 3, \"uid\": 3, \"uidvalidity\": 1, \"msgid\": \"b1@x.test\", \"digest\": \"e770a02a01b39cdd88f99ad8532f1b392e66b48b6126a57e82da85038d06f1cb\", \"mailbox\": \"INBOX\", \"imap_name\": \"INBOX\"}]}]}",
		Preview:     "{\"folder\": \"INBOX\", \"groups\": [{\"name\": \"Bills\", \"count\": 2, \"terms\": [], \"senders\": [{\"label\": \"a@x.test\", \"count\": 2}], \"subjects\": [\"a1\", \"a2\"], \"messages\": [{\"id\": 1, \"subject\": \"a1\", \"from_name\": \"\", \"from_addr\": \"a@x.test\", \"date_ts\": 1791280800.0}, {\"id\": 2, \"subject\": \"a2\", \"from_name\": \"\", \"from_addr\": \"a@x.test\", \"date_ts\": 1791280800.0}]}, {\"name\": \"Travel\", \"count\": 1, \"terms\": [], \"senders\": [{\"label\": \"a@x.test\", \"count\": 1}], \"subjects\": [\"b1\"], \"messages\": [{\"id\": 3, \"subject\": \"b1\", \"from_name\": \"\", \"from_addr\": \"a@x.test\", \"date_ts\": 1791280800.0}]}]}",
	}
	if p != want {
		t.Fatalf("proposal =\n%+v\nwant\n%+v", p, want)
	}
}
