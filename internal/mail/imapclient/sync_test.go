package imapclient

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbtest"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
)

const (
	statusPattern = `C/ ^{t} STATUS %s \((UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)( (UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)){3}\)$`
	fullFetch     = `C/ ^{t} UID FETCH %s \(UID( (FLAGS|INTERNALDATE|RFC822\.SIZE|BODYSTRUCTURE)){4} BODY\.PEEK\[HEADER\] BODY\.PEEK\[TEXT\]<0\.65536>\)$`
	flagFetch     = `C/ ^{t} UID FETCH 1:\* \((UID FLAGS|FLAGS UID)\) \(CHANGEDSINCE %d\)$`
)

func syncStatus(name string, messages, next, validity, modseq int) string {
	return fmt.Sprintf(statusPattern, scriptName(name)) + fmt.Sprintf("\nS: * STATUS %q (MESSAGES %d UIDNEXT %d UIDVALIDITY %d HIGHESTMODSEQ %d)\nS: {t} OK STATUS completed\n",
		name, messages, next, validity, modseq)
}

func scriptName(name string) string {
	if name == "INBOX" {
		return name
	}
	return `"` + name + `"`
}

func syncExamine(name string, exists, validity int) string {
	quoted := scriptName(name)
	return fmt.Sprintf("C: {t} EXAMINE %s\nS: * %d EXISTS\nS: * OK [UIDVALIDITY %d] UIDs valid\nS: {t} OK [READ-ONLY] EXAMINE completed\n", quoted, exists, validity)
}

func syncSearch(criteria string, uids string) string {
	return fmt.Sprintf("C: {t} UID SEARCH %s\nS: * SEARCH %s\nS: {t} OK SEARCH completed\n", criteria, uids)
}

func syncFull(set string, fetched map[int]string, flags string) string {
	var b strings.Builder
	fmt.Fprintf(&b, fullFetch+"\n", set)
	seq := 0
	for _, uid := range slices.Sorted(maps.Keys(fetched)) {
		seq++
		fmt.Fprintf(&b, "S: * %d FETCH (UID %d FLAGS (%s) INTERNALDATE \" 6-Oct-2026 10:00:00 +0000\" RFC822.SIZE 34 "+
			`BODYSTRUCTURE ("text" "plain" ("charset" "utf-8") NIL NIL "7bit" 4 1 NIL NIL NIL NIL) BODY[HEADER] {30}`+"\n", seq, uid, flags)
		b.WriteString("S: From: a@x.test\nS: Subject: " + fetched[uid] + "\nS:  BODY[TEXT]<0> {4}\nS: body)\n")
	}
	b.WriteString("S: {t} OK FETCH completed\n")
	return b.String()
}

const syncList = `C: {t} LIST "" "*"
S: * LIST (\HasNoChildren) "/" INBOX
S: * LIST (\HasNoChildren) "/" "Bills"
S: * LIST (\HasNoChildren \Sent) "/" "Sent Messages"
S: * LIST (\HasNoChildren \Trash) "/" "Deleted Messages"
S: {t} OK LIST completed
`

func TestSyncOverScriptedServer(t *testing.T) {
	ctx := context.Background()
	script := icloudLogin +
		syncList +
		syncStatus("Bills", 2, 3, 5, 10) + syncExamine("Bills", 2, 5) + syncSearch("ALL", "1 2") +
		syncFull("1:2", map[int]string{1: "Ant", 2: "Bee"}, `\Seen`) +
		syncStatus("Sent Messages", 1, 2, 6, 3) + syncExamine("Sent Messages", 1, 6) + syncSearch(`SINCE "17-Jul-2026"`, "1") +
		syncFull("1", map[int]string{1: "Out"}, `\Seen`) +
		syncStatus("INBOX", 2, 9, 7, 20) + syncExamine("INBOX", 2, 7) + syncSearch("ALL", "7 8") +
		syncFull("7:8", map[int]string{7: "Cat", 8: "Dog"}, "") +
		syncList +
		syncStatus("Bills", 1, 3, 5, 12) + syncExamine("Bills", 1, 5) + syncSearch("ALL", "2") +
		fmt.Sprintf(flagFetch, 10) + "\nS: * 1 FETCH (UID 2 MODSEQ (12) FLAGS (\\Seen \\Flagged))\nS: {t} OK FETCH completed\n" +
		syncStatus("Sent Messages", 1, 2, 6, 3) + syncExamine("Sent Messages", 1, 6) + syncSearch(`SINCE "17-Jul-2026"`, "1") +
		syncStatus("INBOX", 3, 10, 7, 21) + syncExamine("INBOX", 3, 7) + syncSearch("ALL", "7 8 9") +
		fmt.Sprintf(flagFetch, 20) + "\nS: * 1 FETCH (UID 7 MODSEQ (21) FLAGS (\\Seen))\nS: {t} OK FETCH completed\n" +
		syncFull("9", map[int]string{9: "Eel"}, "") +
		logout
	client, _ := icloud(t, script, live)
	must(t, client.Connect(ctx))

	dir := t.TempDir()
	env := config.Map(map[string]string{"ATRIUM_DB": filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache")})
	cfg, err := config.Load(env)
	must(t, err)
	d := dbtest.Schema(t)
	clock := 1792080000.0
	s := &mailsync.Syncer{DB: d, Cfg: cfg, Now: func() float64 { return clock }}

	first, err := s.Account(ctx, "icloud", client)
	must(t, err)
	if first.New != 5 || first.Mailboxes != 3 || len(first.GoneIDs) != 0 || first.FlagChanges != 0 {
		t.Fatalf("first sync = %+v", first)
	}
	clock += 3600
	second, err := s.Account(ctx, "icloud", client)
	must(t, err)
	if second.New != 1 || second.Mailboxes != 3 || len(second.GoneIDs) != 1 || second.FlagChanges != 2 {
		t.Fatalf("second sync = %+v", second)
	}

	subjects := map[string]string{}
	rows, err := d.Read.Query("SELECT m.subject, b.name, m.gone_at, m.flags, m.raw_path FROM messages m JOIN mailboxes b ON b.id = m.mailbox_id")
	must(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var subject, box, flags, rawPath string
		var gone *float64
		must(t, rows.Scan(&subject, &box, &gone, &flags, &rawPath))
		state := "live"
		if gone != nil {
			state = "gone"
		}
		subjects[subject] = box + " " + state + " " + flags
		raw, err := os.ReadFile(filepath.Join(cfg.CacheDir, rawPath))
		must(t, err)
		if want := "From: a@x.test\r\nSubject: " + subject + "\r\nbody"; string(raw) != want {
			t.Errorf("%s raw = %q, want %q", subject, raw, want)
		}
	}
	must(t, rows.Err())
	want := map[string]string{
		"Ant": `Bills gone ["\\Seen"]`,
		"Bee": `Bills live ["\\Flagged", "\\Seen"]`,
		"Out": `Sent Messages live ["\\Seen"]`,
		"Cat": `INBOX live ["\\Seen"]`,
		"Dog": `INBOX live []`,
		"Eel": `INBOX live []`,
	}
	if !reflect.DeepEqual(subjects, want) {
		t.Fatalf("messages = %v\nwant %v", subjects, want)
	}
	var kinds string
	must(t, d.Read.QueryRow("SELECT group_concat(kind || ':' || detail, ';') FROM (SELECT kind, detail FROM events ORDER BY id)").Scan(&kinds))
	if !strings.Contains(kinds, `flags:{"flags": [["\\Seen"], ["\\Flagged", "\\Seen"]]`) || !strings.Contains(kinds, `delete:{"from": "Bills"}`) {
		t.Errorf("events = %s", kinds)
	}
	client.Close()
}
