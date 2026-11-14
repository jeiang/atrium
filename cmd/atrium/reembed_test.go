package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
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

var progressRE = regexp.MustCompile(`data-progress>(\d+) of (\d+)<`)

func embedProgress(t *testing.T, base string) (done, total int, page string) {
	t.Helper()
	_, page, _ = fetch(t, http.DefaultClient, http.MethodGet, base+"/settings/mail", nil)
	m := progressRE.FindStringSubmatch(page)
	if m == nil {
		return -1, -1, page
	}
	done, _ = strconv.Atoi(m[1])
	total, _ = strconv.Atoi(m[2])
	return done, total, page
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
	seedNotionVectors(t, path, db.Key{})
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

func TestAFirstServeWithANewDimensionRebuildsTheIndexInsteadOfAdoptingIt(t *testing.T) {
	t.Parallel()
	fake := newFakeEmbedder(t)
	dir, path := goldenEmbedFixture(t, fake, bigModel)
	fake.release(1000)
	base, _, stop := startServeStoppable(t, dir)
	waitFor(t, func() bool {
		_, _, page := embedProgress(t, base)
		return strings.Contains(page, bigModel+" · 1024 dimensions") && !strings.Contains(page, "Re-embedding")
	})
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
	if got := runApp(t, "notion", "search", "project notes"); got.code != cli.ExitOK || got.stdout == "" || fake.lastQueryModel() != bigModel {
		t.Fatalf("search on the rebuilt index: %d %q %q (query model %q)", got.code, got.stdout, got.stderr, fake.lastQueryModel())
	}
}

func TestChangingTheEmbedderReembedsInTheBackgroundAndSwapsWhenComplete(t *testing.T) {
	t.Parallel()
	fake := newFakeEmbedder(t)
	dir, path := goldenEmbedFixture(t, fake, "embeddinggemma")
	search := func() string {
		t.Helper()
		got := runApp(t, "notion", "search", "project notes")
		if got.code != cli.ExitOK {
			t.Fatalf("notion search: %d %q", got.code, got.stderr)
		}
		return got.stdout
	}
	before := search()
	if before == "" {
		t.Fatal("notion search on the golden copy found nothing")
	}

	base, _, stop := startServeStoppable(t, dir)
	waitFor(t, func() bool {
		_, _, page := embedProgress(t, base)
		return strings.Contains(page, "embeddinggemma · 768 dimensions")
	})
	stop()
	if after := search(); after != before || fake.lastQueryModel() != "embeddinggemma" {
		t.Fatalf("search with an unchanged embedder: %q, was %q (model %q)", after, before, fake.lastQueryModel())
	}

	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHost(t, dir, fake.URL+"/v1", bigModel, ""))
	fake.release(1)
	base, _, stop = startServeStoppable(t, dir)
	var first, total int
	waitFor(t, func() bool {
		var page string
		first, total, page = embedProgress(t, base)
		return first > 0 && first < total && strings.Contains(page, "embeddinggemma · 768 dimensions")
	})
	if during := search(); during != before || fake.lastQueryModel() != "embeddinggemma" {
		t.Fatalf("search while re-embedding (%d of %d): %q, was %q (query model %q)", first, total, during, before, fake.lastQueryModel())
	}
	fake.release(1)
	waitFor(t, func() bool {
		done, _, _ := embedProgress(t, base)
		return done > first
	})
	stop()

	fake.release(1000)
	base, _, stop = startServeStoppable(t, dir)
	waitFor(t, func() bool {
		_, _, page := embedProgress(t, base)
		return strings.Contains(page, bigModel+" · 1024 dimensions") && !strings.Contains(page, "Re-embedding")
	})
	stop()

	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var rows, generations, oldTables int
	if err := d.Read.QueryRow("SELECT (SELECT COUNT(*) FROM embed_message_1) + (SELECT COUNT(*) FROM embed_notion_1), "+
		"(SELECT COUNT(*) FROM embed_index), (SELECT COUNT(*) FROM sqlite_master WHERE name IN ('embed_message_0', 'embed_notion_0'))").
		Scan(&rows, &generations, &oldTables); err != nil {
		t.Fatal(err)
	}
	if rows != total || generations != 1 || oldTables != 0 {
		t.Fatalf("after the swap: %d vectors for %d documents, %d index rows, %d old tables", rows, total, generations, oldTables)
	}
	if sent := fake.embedded(bigModel); sent > total+endpoint.TuningMailEmbedBatch+8 {
		t.Errorf("the restart re-embedded finished rows: %d texts sent for %d documents", sent, total)
	}
	if after := search(); after == "" || fake.lastQueryModel() != bigModel {
		t.Errorf("search after the swap: %q (query model %q)", after, fake.lastQueryModel())
	}
}
