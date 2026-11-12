package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
)

const ledgerMarker = "the ledger code is 7712"

func kbEmbedFixture(t *testing.T) (string, *fakeEmbedder, *modelLog, *db.DB) {
	t.Helper()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	fake := newFakeEmbedder(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHost(t, dir, fake.URL+"/v1", "embeddinggemma", ""))
	return dir, fake, api, mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
}

func createLedger(t *testing.T, dir string, fake *fakeEmbedder, body string) (int64, *asstWeb) {
	t.Helper()
	fake.setGate(func(ctx context.Context, texts []string) {
		if len(texts) == 1 && (texts[0] == "atrium probe" || strings.HasPrefix(texts[0], "task: search result")) {
			return
		}
		<-ctx.Done()
	})
	defer fake.setGate(nil)
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := asstBrowse(t, base)
	return knowledgeCreate(t, w, url.Values{
		"title": {"Ledger"}, "body": {body}, "scope": {"work"},
	}), w
}

func recordHash(t *testing.T, d *db.DB, id int64) (sql.NullString, int) {
	t.Helper()
	var hash sql.NullString
	var vectors int
	if err := d.Read.QueryRow("SELECT embedded_hash FROM kb_records WHERE id = ?", id).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM embed_kb_0 WHERE rowid = ?", id).Scan(&vectors); err != nil {
		t.Fatal(err)
	}
	return hash, vectors
}

func documentHash(t *testing.T, d *db.DB, id int64) string {
	t.Helper()
	var title, aliases, body string
	if err := d.Read.QueryRow("SELECT title, aliases, body FROM kb_records WHERE id = ?", id).Scan(&title, &aliases, &body); err != nil {
		t.Fatal(err)
	}
	return embed.Hash(embed.KBDocumentText(context.Background(), title, aliases, body))
}

func TestKnowledgeEditDuringEmbeddingKeepsTheRecordPending(t *testing.T) {
	dir, fake, _, d := kbEmbedFixture(t)
	id, _ := createLedger(t, dir, fake, "old body "+ledgerMarker)

	var mu sync.Mutex
	calls := 0
	first := make(chan string, 1)
	second := make(chan string, 1)
	release := make(chan struct{})
	fake.setGate(func(ctx context.Context, texts []string) {
		if len(texts) == 1 && (texts[0] == "atrium probe" || strings.HasPrefix(texts[0], "task: search result")) {
			return
		}
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		switch n {
		case 1:
			first <- strings.Join(texts, "\n")
		case 2:
			second <- strings.Join(texts, "\n")
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
	})

	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := asstBrowse(t, base)
	select {
	case text := <-first:
		if !strings.Contains(text, ledgerMarker) {
			t.Fatalf("the first embedding request lacks the record text: %q", text)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the first embedding request never arrived")
	}
	newBody := "new body " + ledgerMarker + " with a different detail"
	if code, body, _ := w.post(fmt.Sprintf("/library/knowledge/%d", id), url.Values{
		"title": {"Ledger"}, "body": {newBody}, "scope": {"work"},
	}, false); code != http.StatusSeeOther {
		t.Fatalf("edit during the request = %d: %s", code, body)
	}
	release <- struct{}{}
	select {
	case text := <-second:
		if !strings.Contains(text, newBody) {
			t.Fatalf("the next pass embedded %q, want the edited text", text)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the next pass did not embed the edited record")
	}
	if hash, vectors := recordHash(t, d, id); hash.Valid || vectors != 0 {
		t.Errorf("while the edited text is in flight the record has hash %v and %d vectors; want pending", hash, vectors)
	}
	release <- struct{}{}
	waitFor(t, func() bool {
		hash, vectors := recordHash(t, d, id)
		return hash.Valid && vectors == 1
	})
	hash, _ := recordHash(t, d, id)
	if want := documentHash(t, d, id); hash.String != want {
		t.Errorf("stored hash %q, want the edited document hash %q", hash.String, want)
	}
}

func TestKnowledgeRetractDuringEmbeddingLeavesNoVector(t *testing.T) {
	dir, fake, api, d := kbEmbedFixture(t)
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	id, _ := createLedger(t, dir, fake, "old body "+ledgerMarker)

	first := make(chan string, 1)
	second := make(chan string, 1)
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	fake.setGate(func(ctx context.Context, texts []string) {
		if len(texts) == 1 && (texts[0] == "atrium probe" || strings.HasPrefix(texts[0], "task: search result")) {
			return
		}
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		switch n {
		case 1:
			first <- strings.Join(texts, "\n")
		case 2:
			second <- strings.Join(texts, "\n")
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
	})

	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := asstBrowse(t, base)
	select {
	case <-first:
	case <-time.After(30 * time.Second):
		t.Fatal("the embedding request never arrived")
	}
	if code, body, _ := w.post(fmt.Sprintf("/library/knowledge/%d/retract", id), url.Values{}, false); code != http.StatusSeeOther {
		t.Fatalf("retract during the request = %d: %s", code, body)
	}
	knowledgeCreate(t, w, url.Values{
		"title": {"Sentinel"}, "body": {"sentinel " + ledgerMarker}, "scope": {"work"},
	})
	release <- struct{}{}
	select {
	case text := <-second:
		if !strings.Contains(text, "sentinel") {
			t.Fatalf("the fill embedded %q after the retract, want the sentinel record", text)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the fill did not move past the retracted record")
	}
	if hash, vectors := recordHash(t, d, id); vectors != 0 {
		t.Errorf("the retracted record kept %d vectors (hash %v)", vectors, hash)
	}
	var retracted sql.NullFloat64
	if err := d.Read.QueryRow("SELECT retracted_at FROM kb_records WHERE id = ?", id).Scan(&retracted); err != nil || !retracted.Valid {
		t.Fatalf("record retracted_at = %v, %v", retracted, err)
	}
	release <- struct{}{}

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "kb_search", map[string]any{"query": "ledger"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("nothing")))
	if ran := runApp(t, "agent", "run", "--text", "what is the ledger code"); ran.code != cli.ExitOK {
		t.Fatalf("kb_search run: %d %s", ran.code, ran.stderr)
	}
	for _, raw := range kbResults(t, d, latestThread(t, dir), "s1") {
		hit, _ := raw.(map[string]any)
		if hit["cite"] == fmt.Sprintf("K%d", id) {
			t.Errorf("kb_search returned the retracted record: %v", hit)
		}
	}
}

func TestKnowledgeEditAfterEmbeddingRequeuesTheRecord(t *testing.T) {
	dir, fake, _, d := kbEmbedFixture(t)
	id, _ := createLedger(t, dir, fake, "old body "+ledgerMarker)

	base, _, stop := startServeStoppable(t, dir)
	w := asstBrowse(t, base)
	waitFor(t, func() bool {
		hash, vectors := recordHash(t, d, id)
		return hash.Valid && vectors == 1
	})
	before, _ := recordHash(t, d, id)

	newBody := "edited body " + ledgerMarker
	if code, body, _ := w.post(fmt.Sprintf("/library/knowledge/%d", id), url.Values{
		"title": {"Ledger"}, "body": {newBody}, "scope": {"work"},
	}, false); code != http.StatusSeeOther {
		t.Fatalf("edit = %d: %s", code, body)
	}
	after, vectors := recordHash(t, d, id)
	if after.String != before.String || vectors != 1 {
		t.Fatalf("after the edit the record has hash %q and %d vectors, want the old hash and vector until the next pass", after.String, vectors)
	}
	if after.String == documentHash(t, d, id) {
		t.Fatal("the edited record still hashes to its embedded text")
	}
	stop()

	sent := make(chan string, 1)
	release := make(chan struct{})
	fake.setGate(func(ctx context.Context, texts []string) {
		if len(texts) == 1 && (texts[0] == "atrium probe" || strings.HasPrefix(texts[0], "task: search result")) {
			return
		}
		select {
		case sent <- strings.Join(texts, "\n"):
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
	})
	_, _, stop = startServeStoppable(t, dir)
	defer stop()
	select {
	case text := <-sent:
		if !strings.Contains(text, newBody) {
			t.Fatalf("the next pass embedded %q, want the edited text", text)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the edited record was not re-embedded")
	}
	release <- struct{}{}
	waitFor(t, func() bool {
		hash, count := recordHash(t, d, id)
		return hash.Valid && count == 1 && hash.String == documentHash(t, d, id)
	})
}
