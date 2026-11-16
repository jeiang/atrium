package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/testutil/embedstub"
	"github.com/jeiang/atrium/internal/testutil/mailscript"
)

const signalsWant = `refresh: {"commitments":1,"loops":{"owe_reply":2,"waiting_on_them":1},"reminders":1}
loop|test|owe_reply|5|friend@example.test|Dinner?
loop|test|owe_reply|6|colleague@example.test|Bcc intro
loop|test|waiting_on_them|9|sam@example.test|Contract draft
commitment|5|Reply to Friend about dinner|me|<due>|open|proposed
proposal|reminder|pending|Remind me: Reply to Friend about dinner (due <due>)|{"commitment_id":1,"due":"<due>","message_id":5,"owner":"me","remind_at":"TS","subject":"Dinner?","text":"Reply to Friend about dinner","tz":"TS"}
GET /api/v1/mail/loops: {"items":[{"account":"test","counterpart":"sam@example.test","days":10,"dismissed":0,"id":1,"last_message_id":9,"since":"TS","snoozed_until":null,"state":"waiting_on_them","subject":"Contract draft","thread_key":"t:id9"},{"account":"test","counterpart":"friend@example.test","days":1,"dismissed":0,"id":2,"last_message_id":5,"since":"TS","snoozed_until":null,"state":"owe_reply","subject":"Dinner?","thread_key":"t:id5"},{"account":"test","counterpart":"colleague@example.test","days":1,"dismissed":0,"id":3,"last_message_id":6,"since":"TS","snoozed_until":null,"state":"owe_reply","subject":"Bcc intro","thread_key":"t:id6"}],"next_cursor":null,"snooze_default":3}
GET /api/v1/mail/commitments: {"items":[{"due":"<due>","from_addr":"friend@example.test","id":1,"message_id":5,"owner":"me","proposal_id":1,"status":"open","subject":"Dinner?","text":"Reply to Friend about dinner"}],"next_cursor":null}
`

func signalsNormalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, inner := range x {
			switch k {
			case "since", "updated_ts", "created_ts", "remind_at", "tz":
				if inner != nil {
					x[k] = "TS"
				}
			default:
				x[k] = signalsNormalize(inner)
			}
		}
	case []any:
		for i := range x {
			x[i] = signalsNormalize(x[i])
		}
	}
	return v
}

func signalsJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	out, err := json.Marshal(signalsNormalize(v))
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestSignalsRefreshAndReadsMatchTheStoredLoopsCommitmentsAndReminders(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	script := mailscript.Load(t)
	due := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	api.AnswerJSON(func(required []string, user string) (json.RawMessage, error) {
		if slices.Equal(required, []string{"commitments"}) {
			if strings.Contains(user, "Subject: Dinner?") {
				return json.RawMessage(`{"commitments":[{"text":"Reply to Friend about dinner","owner":"me","due":"` + due + `"}]}`), nil
			}
			return json.RawMessage(`{"commitments":[]}`), nil
		}
		return script.JSONAnswer(required, user)
	})
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHost(t, dir, embedstub.Server(t).URL+"/v1", "embeddinggemma", ""))
	if ran := runApp(t, "settings", "list"); ran.code != cli.ExitOK {
		t.Fatalf("initialize database: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	for _, q := range []string{
		"INSERT INTO accounts (name, kind) VALUES ('test', 'icloud')",
		"INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (1, 'test', 'INBOX', 'INBOX', 'inbox')",
	} {
		if _, err := d.Write.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	cacheDir := filepath.Join(dir, "cache")
	for _, m := range []seededLoopMail{
		{uid: 1, ageDays: 100, sent: true, subject: "Earlier note", from: "owner@example.test", fromDomain: "example.test", toAddrs: `["friend@example.test"]`, esp: "[]",
			raw: "From: Owner <owner@example.test>\r\nTo: Friend <friend@example.test>\r\nSubject: Earlier note\r\n\r\nHello.\r\n"},
		{uid: 2, ageDays: 1, subject: "Weekly news", from: "weekly@example.test", fromDomain: "example.test", toAddrs: `["owner@example.test"]`, esp: "[]",
			raw: "From: Weekly <weekly@example.test>\r\nTo: Owner <owner@example.test>\r\nList-Unsubscribe: <https://example.test/u>\r\nSubject: Weekly news\r\n\r\nStories.\r\n"},
		{uid: 3, ageDays: 1, subject: "Your order shipped", from: "dispatch@example.test", fromDomain: "example.test", toAddrs: `["owner@example.test"]`, esp: `["mailgun"]`,
			raw: "From: Dispatch <dispatch@example.test>\r\nTo: Owner <owner@example.test>\r\nSubject: Your order shipped\r\n\r\nOn its way.\r\n"},
		{uid: 4, ageDays: 1, subject: "Group thread", from: "colleague@example.test", fromDomain: "example.test", toAddrs: `["other@example.test","owner@example.test"]`, esp: "[]",
			raw: "From: Colleague <colleague@example.test>\r\nTo: Other <other@example.test>\r\nCc: Owner <owner@example.test>\r\nSubject: Group thread\r\n\r\nFor the group.\r\n"},
		{uid: 5, ageDays: 1, subject: "Dinner?", from: "friend@example.test", fromDomain: "example.test", toAddrs: `["owner@example.test"]`, esp: "[]",
			raw: "From: Friend <friend@example.test>\r\nTo: Owner <owner@example.test>\r\nSubject: Dinner?\r\n\r\nCan you reply by Friday?\r\n"},
		{uid: 6, ageDays: 1, subject: "Bcc intro", from: "colleague@example.test", fromDomain: "example.test", toAddrs: `[]`, esp: "[]",
			raw: "From: Colleague <colleague@example.test>\r\nSubject: Bcc intro\r\n\r\nCan we talk this week?\r\n"},
		{uid: 7, ageDays: 1, subject: "Auto reply", from: "noreply@example.test", fromDomain: "example.test", toAddrs: `["owner@example.test"]`, esp: "[]",
			raw: "From: Robot <noreply@example.test>\r\nTo: Owner <owner@example.test>\r\nAuto-Submitted: auto-replied\r\nSubject: Auto reply\r\n\r\nAway.\r\n"},
		{uid: 8, ageDays: 1, subject: "Owner draft", from: "owner@example.test", fromDomain: "example.test", toAddrs: `["sam@example.test"]`, esp: "[]",
			raw: "From: Owner <owner@example.test>\r\nTo: Sam <sam@example.test>\r\nSubject: Owner draft\r\n\r\nDraft.\r\n"},
		{uid: 9, ageDays: 10, sent: true, subject: "Contract draft", from: "owner@example.test", fromDomain: "example.test", toAddrs: `["sam@example.test"]`, esp: "[]",
			raw: "From: Owner <owner@example.test>\r\nTo: Sam <sam@example.test>\r\nSubject: Contract draft\r\n\r\nPlease review.\r\n"},
	} {
		seedLoopMail(t, d, cacheDir, m)
	}
	if _, err := d.Write.Exec(`UPDATE messages SET labels = '["\\Draft"]' WHERE subject = 'Owner draft'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("UPDATE messages SET msgid = '' WHERE account = 'test'"); err != nil {
		t.Fatal(err)
	}
	ran := runApp(t, "loops", "refresh")
	if ran.code != cli.ExitOK {
		t.Fatalf("loops refresh: %d %s", ran.code, ran.stderr)
	}
	var got strings.Builder
	got.WriteString("refresh: " + signalsJSON(t, []byte(ran.stdout)) + "\n")
	dump := func(prefix, query string) {
		t.Helper()
		rows, err := d.Read.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
			cols, err := r.Columns()
			if err != nil {
				return "", err
			}
			vals := make([]sql.NullString, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := r.Scan(ptrs...); err != nil {
				return "", err
			}
			parts := []string{prefix}
			for _, v := range vals {
				parts = append(parts, v.String)
			}
			return strings.Join(parts, "|"), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range lines {
			got.WriteString(line + "\n")
		}
	}
	dump("loop", "SELECT l.account, l.state, m.uid, l.counterpart, l.subject FROM open_loops l JOIN messages m ON m.id = l.last_message_id ORDER BY l.state, m.uid")
	dump("commitment", "SELECT m.uid, c.text, c.owner, c.due, c.status, CASE WHEN c.proposal_id IS NULL THEN 'none' ELSE 'proposed' END FROM commitments c JOIN messages m ON m.id = c.message_id ORDER BY c.id")
	rows, err := d.Read.Query("SELECT kind, status, title, payload FROM proposals ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	type proposal struct{ kind, status, title, payload string }
	found, err := db.Collect(rows, func(r *sql.Rows) (proposal, error) {
		var p proposal
		return p, r.Scan(&p.kind, &p.status, &p.title, &p.payload)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range found {
		var payload map[string]any
		if err := json.Unmarshal([]byte(p.payload), &payload); err != nil {
			t.Fatal(err)
		}
		if p.kind == "reminder" {
			var subject string
			if err := d.Read.QueryRow("SELECT subject FROM messages WHERE id = ?", payload["message_id"]).Scan(&subject); err != nil {
				t.Fatal(err)
			}
			var uid int64
			if err := d.Read.QueryRow("SELECT uid FROM messages WHERE id = ?", payload["message_id"]).Scan(&uid); err != nil {
				t.Fatal(err)
			}
			payload["message_id"], payload["subject"] = uid, subject
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		got.WriteString("proposal|" + p.kind + "|" + p.status + "|" + p.title + "|" + signalsJSON(t, raw) + "\n")
	}
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}
	for _, path := range []string{"/api/v1/mail/loops", "/api/v1/mail/commitments"} {
		data, err := json.Marshal(apiData(t, s.do(http.MethodGet, path, token, nil), http.StatusOK))
		if err != nil {
			t.Fatal(err)
		}
		got.WriteString("GET " + path + ": " + signalsJSON(t, data) + "\n")
	}
	if out := strings.ReplaceAll(got.String(), due, "<due>"); out != signalsWant {
		t.Errorf("signals output changed:\n%s", out)
	}
}
