package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/testutil/llmstub"
)

const loopDay = 86400.0

type seededLoopMail struct {
	uid        int
	ageDays    float64
	sent       bool
	subject    string
	from       string
	fromDomain string
	toAddrs    string
	esp        string
	raw        string
	noRaw      bool
}

func seedLoopMail(t *testing.T, d *db.DB, cacheDir string, m seededLoopMail) {
	t.Helper()
	rel := fmt.Sprintf("raw/test/%d.eml", m.uid)
	if !m.noRaw {
		path := filepath.Join(cacheDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(m.raw), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	now := float64(time.Now().Unix())
	if _, err := d.Write.Exec(
		"INSERT INTO messages (account, mailbox_id, uidvalidity, uid, digest, date_ts, sent, subject, from_addr, from_domain, to_addrs, esp, raw_path, first_seen) "+
			"VALUES ('test', 1, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		m.uid, fmt.Sprintf("digest-%d", m.uid), now-m.ageDays*loopDay, m.sent, m.subject, m.from, m.fromDomain,
		m.toAddrs, m.esp, rel, now); err != nil {
		t.Fatal(err)
	}
}

func TestLoopsRefreshSkipsBulkAndOtherRecipients(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	var mu sync.Mutex
	var bodies []string
	srv := llmstub.Server(t, func(feature string, body []byte) bool {
		if feature == "commitments" {
			mu.Lock()
			bodies = append(bodies, string(body))
			mu.Unlock()
		}
		return true
	})
	setEnv(t, settingEnv(endpoint.KeyChatURL), srv.URL+"/v1")
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHost(t, dir, srv.URL+"/v1", "embeddinggemma", ""))
	if ran := runApp(t, "settings", "list"); ran.code != cli.ExitOK {
		t.Fatalf("initialize database: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	if _, err := d.Write.Exec("INSERT INTO accounts (name, kind) VALUES ('test', 'icloud')"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (1, 'test', 'INBOX', 'INBOX', 'inbox')"); err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(dir, "cache")
	seedLoopMail(t, d, cacheDir, seededLoopMail{
		uid: 1, ageDays: 100, sent: true, subject: "Earlier note", from: "owner@example.test", fromDomain: "example.test",
		toAddrs: `["friend@example.test"]`, esp: "[]",
		raw: "From: Owner <owner@example.test>\r\nTo: Friend <friend@example.test>\r\nSubject: Earlier note\r\n\r\nHello.\r\n",
	})
	seedLoopMail(t, d, cacheDir, seededLoopMail{
		uid: 2, ageDays: 1, subject: "Weekly news", from: "weekly@example.test", fromDomain: "example.test",
		toAddrs: `["owner@example.test"]`, esp: "[]",
		raw: "From: Weekly <weekly@example.test>\r\nTo: Owner <owner@example.test>\r\nList-Unsubscribe: <https://example.test/unsubscribe>\r\nSubject: Weekly news\r\n\r\nStories.\r\n",
	})
	seedLoopMail(t, d, cacheDir, seededLoopMail{
		uid: 3, ageDays: 1, subject: "Your order shipped", from: "dispatch@example.test", fromDomain: "example.test",
		toAddrs: `["owner@example.test"]`, esp: `["mailgun"]`,
		raw: "From: Dispatch <dispatch@example.test>\r\nTo: Owner <owner@example.test>\r\nSubject: Your order shipped\r\n\r\nOn its way.\r\n",
	})
	seedLoopMail(t, d, cacheDir, seededLoopMail{
		uid: 4, ageDays: 1, subject: "Group thread", from: "colleague@example.test", fromDomain: "example.test",
		toAddrs: `["other@example.test","owner@example.test"]`, esp: "[]",
		raw: "From: Colleague <colleague@example.test>\r\nTo: Other <other@example.test>\r\nCc: Owner <owner@example.test>\r\nSubject: Group thread\r\n\r\nFor the group.\r\n",
	})
	seedLoopMail(t, d, cacheDir, seededLoopMail{
		uid: 5, ageDays: 1, subject: "Dinner?", from: "friend@example.test", fromDomain: "example.test",
		toAddrs: `["owner@example.test"]`, esp: "[]",
		raw: "From: Friend <friend@example.test>\r\nTo: Owner <owner@example.test>\r\nSubject: Dinner?\r\n\r\nCan you reply by Friday?\r\n",
	})
	seedLoopMail(t, d, cacheDir, seededLoopMail{
		uid: 6, ageDays: 1, subject: "Bcc intro", from: "colleague@example.test", fromDomain: "example.test",
		toAddrs: `[]`, esp: "[]",
		raw: "From: Colleague <colleague@example.test>\r\nSubject: Bcc intro\r\n\r\nCan we talk this week?\r\n",
	})
	seedLoopMail(t, d, cacheDir, seededLoopMail{
		uid: 7, ageDays: 1, subject: "Mail merge", from: "friend@example.test", fromDomain: "example.test",
		toAddrs: `["other@example.test"]`, esp: "[]",
		raw: "From: Friend <friend@example.test>\r\nTo: Other <other@example.test>\r\nSubject: Mail merge\r\n\r\nAre you free next week?\r\n",
	})
	seedLoopMail(t, d, cacheDir, seededLoopMail{
		uid: 8, ageDays: 1, subject: "Bcc uncached", from: "friend@example.test", fromDomain: "example.test",
		toAddrs: `["other@example.test"]`, esp: "[]", noRaw: true,
	})
	if ran := runApp(t, "loops", "refresh"); ran.code != cli.ExitOK {
		t.Fatalf("loops refresh: %d %s", ran.code, ran.stderr)
	}
	rows, err := d.Read.Query("SELECT subject FROM open_loops ORDER BY subject")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var subjects []string
	for rows.Next() {
		var subject string
		if err := rows.Scan(&subject); err != nil {
			t.Fatal(err)
		}
		subjects = append(subjects, subject)
	}
	if len(subjects) != 4 || subjects[0] != "Bcc intro" || subjects[1] != "Bcc uncached" || subjects[2] != "Dinner?" || subjects[3] != "Mail merge" {
		t.Fatalf("open loops = %v, want the direct question, the BCC, the uncached BCC and the mail merge", subjects)
	}
	var scans int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM mail_scans WHERE kind = 'commitments'").Scan(&scans); err != nil {
		t.Fatal(err)
	}
	if scans != 4 {
		t.Fatalf("commitment scans = %d, want the four wanted messages", scans)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 4 {
		t.Fatalf("commitment prompts = %d, want 4", len(bodies))
	}
	for _, body := range bodies {
		for _, want := range []string{"owner@example.test", "ignore items meant for other recipients"} {
			if !strings.Contains(body, want) {
				t.Fatalf("commitment prompt misses %q: %s", want, body)
			}
		}
	}
}
