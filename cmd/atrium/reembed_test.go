package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const bigModel = "wide-embedder"

type fakeEmbedder struct {
	*httptest.Server
	mu         sync.Mutex
	allow      int
	texts      map[string]int
	queryModel string
	gate       func(ctx context.Context, texts []string)
}

func newFakeEmbedder(t *testing.T) *fakeEmbedder {
	f := &fakeEmbedder{texts: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = w.Write([]byte(`{"data":[{"id":"embeddinggemma"},{"id":"` + bigModel + `"}]}`))
			return
		}
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		dim := config.EmbedDim
		if req.Model == bigModel {
			dim = 1024
			for !slices.Equal(req.Input, []string{"atrium probe"}) && !f.take() {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(10 * time.Millisecond):
				}
			}
		}
		f.mu.Lock()
		gate := f.gate
		f.mu.Unlock()
		if gate != nil {
			gate(r.Context(), req.Input)
		}
		f.mu.Lock()
		f.texts[req.Model] += len(req.Input)
		for _, text := range req.Input {
			if strings.Contains(text, "task: search result") {
				f.queryModel = req.Model
			}
		}
		f.mu.Unlock()
		data := make([]any, len(req.Input))
		for i, text := range req.Input {
			data[i] = map[string]any{"index": i, "embedding": hashedVector(text, dim)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeEmbedder) setGate(gate func(ctx context.Context, texts []string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gate = gate
}

func (f *fakeEmbedder) take() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.allow <= 0 {
		return false
	}
	f.allow--
	return true
}

func (f *fakeEmbedder) release(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allow += n
}

func (f *fakeEmbedder) embedded(model string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.texts[model]
}

func (f *fakeEmbedder) lastQueryModel() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queryModel
}

func hashedVector(text string, dim int) []float64 {
	v := make([]float64, dim)
	for _, token := range strings.Fields(strings.ToLower(text)) {
		sum := sha256.Sum256([]byte(token))
		v[binary.BigEndian.Uint32(sum[:4])%uint32(dim)]++
	}
	v[0] += 0.5
	return v
}

func embedProgress(t *testing.T, path string) (done, total int, page string) {
	t.Helper()
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	rows, err := d.Read.Query("SELECT state, model, dimension, done, total FROM embed_index ORDER BY generation")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	done, total = -1, -1
	var b strings.Builder
	for rows.Next() {
		var state, model string
		var dimension, built, size int
		if err := rows.Scan(&state, &model, &dimension, &built, &size); err != nil {
			t.Fatal(err)
		}
		if state == "building" {
			done, total = built, size
			b.WriteString("Re-embedding ")
		}
		if model != "" {
			b.WriteString(model + " · ")
		}
		fmt.Fprintf(&b, "%d dimensions\n", dimension)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return done, total, b.String()
}

func goldenEmbedFixture(t *testing.T, fake *fakeEmbedder, model string) (dir, path string) {
	t.Helper()
	dir, _, _ = moduleFixture(t, flagsExcept("", ""))
	data, err := os.ReadFile(filepath.Join(golden.Dir(), "world.db"))
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "atrium.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	seedMessageVectors(t, path)
	seedPendingMessage(t, path)
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHost(t, dir, fake.URL+"/v1", model, ""))
	return dir, path
}

func seedPendingMessage(t *testing.T, path string) {
	t.Helper()
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	asstExec(t, d, "UPDATE jobs SET next_due = NULL")
	dbtest.AddMessage(t, d, 900000, "a pending message for the re-embedding build", map[string]any{
		"subject": "Pending re-embedding message", "from_addr": "seed@x.test",
	})
}

func seedMessageVectors(t *testing.T, path string) {
	t.Helper()
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	type message struct {
		id      int64
		account string
		subject string
	}
	rows, err := d.Read.Query("SELECT id, account, subject FROM messages WHERE gone_at IS NULL AND id IN (SELECT rowid FROM message_fts)")
	if err != nil {
		t.Fatal(err)
	}
	var messages []message
	for rows.Next() {
		var m message
		if err := rows.Scan(&m.id, &m.account, &m.subject); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, m)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if _, err := d.Write.Exec("INSERT INTO embed_message_0 (rowid, account, embedding) VALUES (?, ?, ?)", m.id, m.account, embed.Serialize(hashedVector(m.subject, config.EmbedDim))); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Write.Exec("UPDATE messages SET embedded_hash = 'seeded' WHERE id = ?", m.id); err != nil {
			t.Fatal(err)
		}
	}
}

func gmailSearch(t *testing.T, base, token string) string {
	t.Helper()
	s := &apiSite{t: t, base: base}
	items := apiItems(t, apiData(t, s.do(http.MethodGet, "/api/v1/mail/messages?q=weekly+status&account=gmail", token, nil), http.StatusOK))
	if len(items) == 0 {
		return ""
	}
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestAFirstServeWithANewDimensionRebuildsTheIndexInsteadOfAdoptingIt(t *testing.T) {
	t.Parallel()
	fake := newFakeEmbedder(t)
	dir, path := goldenEmbedFixture(t, fake, bigModel)
	token := localAPIToken(t, "reembed")
	fake.release(1000)
	base, _, stop := startServeStoppable(t, dir)
	waitFor(t, func() bool {
		_, _, page := embedProgress(t, path)
		return strings.Contains(page, bigModel+" · 1024 dimensions") && !strings.Contains(page, "Re-embedding")
	})
	if got := gmailSearch(t, base, token); got == "" || fake.lastQueryModel() != bigModel {
		t.Fatalf("search on the rebuilt index: %q (query model %q)", got, fake.lastQueryModel())
	}
	stop()
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var generation int
	if err := d.Read.QueryRow("SELECT generation FROM embed_index WHERE state = 'active' AND dimension = 1024").Scan(&generation); err != nil || generation != 1 {
		t.Fatalf("active index after the first serve: generation %d (%v)", generation, err)
	}
}

func TestChangingTheEmbedderReembedsInTheBackgroundAndSwapsWhenComplete(t *testing.T) {
	t.Parallel()
	fake := newFakeEmbedder(t)
	dir, path := goldenEmbedFixture(t, fake, "embeddinggemma")
	token := localAPIToken(t, "reembed")

	base, _, stop := startServeStoppable(t, dir)
	waitFor(t, func() bool {
		_, _, page := embedProgress(t, path)
		return strings.Contains(page, "embeddinggemma · 768 dimensions")
	})
	before := gmailSearch(t, base, token)
	if before == "" || fake.lastQueryModel() != "embeddinggemma" {
		t.Fatalf("search with an unchanged embedder: %q (model %q)", before, fake.lastQueryModel())
	}
	stop()

	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHost(t, dir, fake.URL+"/v1", bigModel, ""))
	fake.release(1)
	base, _, stop = startServeStoppable(t, dir)
	var first, total int
	waitFor(t, func() bool {
		var page string
		first, total, page = embedProgress(t, path)
		return first > 0 && first < total && strings.Contains(page, "embeddinggemma · 768 dimensions")
	})
	if during := gmailSearch(t, base, token); during != before || fake.lastQueryModel() != "embeddinggemma" {
		t.Fatalf("search while re-embedding (%d of %d): %q, was %q (query model %q)", first, total, during, before, fake.lastQueryModel())
	}
	fake.release(1)
	waitFor(t, func() bool {
		done, _, _ := embedProgress(t, path)
		return done > first
	})
	stop()

	fake.release(1000)
	_, _, stop = startServeStoppable(t, dir)
	waitFor(t, func() bool {
		_, _, page := embedProgress(t, path)
		return strings.Contains(page, bigModel+" · 1024 dimensions") && !strings.Contains(page, "Re-embedding")
	})
	stop()

	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var rows, generations, oldTables int
	if err := d.Read.QueryRow("SELECT (SELECT COUNT(*) FROM embed_message_1) + (SELECT COUNT(*) FROM embed_kb_1), "+
		"(SELECT COUNT(*) FROM embed_index), (SELECT COUNT(*) FROM sqlite_master WHERE name IN ('embed_message_0', 'embed_kb_0'))").
		Scan(&rows, &generations, &oldTables); err != nil {
		t.Fatal(err)
	}
	if rows != total || generations != 1 || oldTables != 0 {
		t.Fatalf("after the swap: %d vectors for %d documents, %d index rows, %d old tables", rows, total, generations, oldTables)
	}
	if sent := fake.embedded(bigModel); sent > total+endpoint.TuningMailEmbedBatch+8 {
		t.Errorf("the restart re-embedded finished rows: %d texts sent for %d documents", sent, total)
	}
	base, _, stop = startServeStoppable(t, dir)
	if after := gmailSearch(t, base, token); after == "" || fake.lastQueryModel() != bigModel {
		t.Errorf("search after the swap: %q (query model %q)", after, fake.lastQueryModel())
	}
	stop()
}
