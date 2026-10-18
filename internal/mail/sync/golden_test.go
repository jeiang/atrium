package sync_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

type seedMessage struct {
	Key    string   `json:"key"`
	Flags  []string `json:"flags"`
	Labels []string `json:"labels"`
	Header string   `json:"header"`
	Text   string   `json:"text"`
}

type seedAccount struct {
	Mailboxes  map[string][]seedMessage `json:"mailboxes"`
	SpecialUse map[string][]string      `json:"special_use"`
	Validity   uint32                   `json:"uidvalidity"`
}

type seeds struct {
	Accounts   map[string]seedAccount `json:"accounts"`
	Categories map[string][]string    `json:"gmail_categories"`
}

type scenario struct {
	Clocks map[string]float64 `json:"clocks"`
}

type placed struct {
	box string
	uid uint32
}

type account struct {
	fake *mailtest.FakeClient
	uids map[string]placed
}

func loadJSON(t *testing.T, file string, into any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(golden.Dir(), "pipeline", file))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

func seedAccounts(t *testing.T, env config.Env, goldenMailboxes []row) map[string]*account {
	t.Helper()
	var s seeds
	loadJSON(t, "mailboxes.json", &s)
	order := map[string][]string{}
	for _, r := range goldenMailboxes {
		a := r["account"].(string)
		order[a] = append(order[a], r["imap_name"].(string))
	}
	out := map[string]*account{}
	for name, spec := range s.Accounts {
		acc := &account{fake: mailtest.NewFakeClient(env), uids: map[string]placed{}}
		acc.fake.Gmail = name == config.Gmail
		names := slices.Clone(order[name])
		for box := range spec.Mailboxes {
			if !slices.Contains(names, box) {
				names = append(names, box)
			}
		}
		for _, box := range names {
			fb := acc.fake.AddBox(box, mailtest.NewBox(spec.Validity), spec.SpecialUse[box]...)
			for _, m := range spec.Mailboxes[box] {
				uid := fb.Add([]byte(m.Header), []byte(m.Text), m.Flags, m.Labels)
				acc.uids[m.Key] = placed{box, uid}
			}
		}
		acc.fake.CategoryUIDs = map[string][]uint32{}
		if name == config.Gmail {
			for category, keys := range s.Categories {
				for _, k := range keys {
					acc.fake.CategoryUIDs[category] = append(acc.fake.CategoryUIDs[category], acc.uids[k].uid)
				}
			}
		}
		out[name] = acc
	}
	return out
}

func (a *account) move(key, dest string) {
	p := a.uids[key]
	m := a.fake.Boxes[p.box].Messages[p.uid]
	a.uids[key] = placed{dest, a.fake.Boxes[dest].Add(m.Header, m.Text, m.Flags, m.Labels)}
	a.fake.Boxes[p.box].Remove(p.uid)
}

func (a *account) remove(keys ...string) {
	for _, key := range keys {
		p := a.uids[key]
		a.fake.Boxes[p.box].Remove(p.uid)
		delete(a.uids, key)
	}
}

func (a *account) setFlags(key string, flags, labels []string) {
	p := a.uids[key]
	a.fake.Boxes[p.box].SetFlags(p.uid, flags, labels)
}

var mutableColumns = []string{"flags", "seen", "flagged", "labels", "in_inbox", "sent", "location", "folder", "embedded", "gone_at"}

func without(r row, drop ...string) row {
	out := row{}
	for k, v := range r {
		if !slices.Contains(drop, k) {
			out[k] = v
		}
	}
	return out
}

func TestGoldenPipelineSync(t *testing.T) {
	ctx := context.Background()
	var sc scenario
	loadJSON(t, "scenario.json", &sc)
	gd, _ := dbtest.Copy(t, filepath.Join(golden.Dir(), "atrium.db"))
	gold := &harness{t: t, d: gd}
	goldMailboxes := gold.query("SELECT * FROM mailboxes ORDER BY id")

	h := newHarness(t, "shadow")
	accounts := seedAccounts(t, config.Map(map[string]string{"ATRIUM_MODE": "shadow"}), goldMailboxes)
	syncAll := func() {
		t.Helper()
		for _, spec := range config.Accounts {
			_, err := h.s.Account(ctx, spec.Name, accounts[spec.Name].fake)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	compareMessages := func(firstSeen float64) int {
		t.Helper()
		want := gold.messages("first_seen = ?", firstSeen)
		if len(want) == 0 {
			t.Fatalf("no golden messages first seen at %v", firstSeen)
		}
		for _, w := range want {
			got := h.messages("id = ?", w["id"])
			if len(got) != 1 {
				t.Fatalf("message %v missing from the replay", w["id"])
			}
			if d := diffRows(without(w, mutableColumns...), without(got[0], mutableColumns...)); d != "" {
				t.Errorf("message %v: %s", w["id"], d)
			}
			raw, err := os.ReadFile(filepath.Join(h.cfg.CacheDir, w["raw_path"].(string)))
			if err != nil {
				t.Fatal(err)
			}
			wantRaw, err := os.ReadFile(filepath.Join(golden.Dir(), "state", "cache", w["raw_path"].(string)))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, wantRaw) {
				t.Errorf("message %v: raw file differs", w["id"])
			}
			wantFTS := gold.query("SELECT subject, sender, body FROM message_fts WHERE rowid = ?", w["id"])
			gotFTS := h.query("SELECT subject, sender, body FROM message_fts WHERE rowid = ?", w["id"])
			if !reflect.DeepEqual(wantFTS, gotFTS) {
				t.Errorf("message %v: fts row %v, want %v", w["id"], gotFTS, wantFTS)
			}
		}
		return len(want)
	}

	h.clock = sc.Clocks["sync1"]
	syncAll()
	n := compareMessages(sc.Clocks["sync1"])
	if got := h.count("SELECT COUNT(*) FROM messages"); got != int64(n) {
		t.Fatalf("replay holds %d messages after the first sync, golden first sync %d", got, n)
	}
	var keys map[string]string
	loadJSON(t, "message_keys.json", &keys)
	for _, r := range h.query("SELECT id, msgid FROM messages") {
		if want := keys[itoa(r["id"])] + "@golden.example"; r["msgid"] != want {
			t.Errorf("message %v msgid %v, want %v", r["id"], r["msgid"], want)
		}
	}
	wantBoxes := gold.query("SELECT id, account, name, imap_name, role FROM mailboxes ORDER BY id")
	gotBoxes := h.query("SELECT id, account, name, imap_name, role FROM mailboxes ORDER BY id")
	if !reflect.DeepEqual(wantBoxes, gotBoxes) {
		t.Errorf("mailboxes = %v, want %v", gotBoxes, wantBoxes)
	}

	ic, gm := accounts["icloud"], accounts["gmail"]
	ic.move("mystery", "Bills")
	ic.remove("digest-11")
	ic.setFlags("itin-13", []string{`\Seen`, `\Flagged`}, nil)
	gm.remove("flash-1", "flash-2", "flash-3", "flash-4", "flash-5")
	gm.setFlags("gift", nil, []string{`\Inbox`, "Receipts"})
	h.clock = sc.Clocks["sync2"]
	syncAll()
	compareMessages(sc.Clocks["sync2"])
	wantEvents := gold.query("SELECT id, ts, account, kind, message_id, detail FROM events WHERE ts = ? ORDER BY id", sc.Clocks["sync2"])
	gotEvents := h.query("SELECT id, ts, account, kind, message_id, detail FROM events ORDER BY id")
	if len(wantEvents) == 0 || !reflect.DeepEqual(wantEvents, gotEvents) {
		t.Errorf("events = %v, want %v", gotEvents, wantEvents)
	}
	for _, w := range gold.query("SELECT id, gone_at FROM messages WHERE gone_at = ?", sc.Clocks["sync2"]) {
		if got := h.messages("id = ?", w["id"]); len(got) != 1 || got[0]["gone_at"] != w["gone_at"] {
			t.Errorf("message %v gone_at = %v, want %v", w["id"], got, w["gone_at"])
		}
	}
	if n := len(ic.fake.Writes) + len(gm.fake.Writes); n != 0 {
		t.Errorf("sync wrote %d times", n)
	}
}

func diffRows(want, got row) string {
	for k, w := range want {
		if g, ok := got[k]; !ok || !reflect.DeepEqual(w, g) {
			return k + ": " + show(g) + ", want " + show(w)
		}
	}
	return ""
}

func show(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
