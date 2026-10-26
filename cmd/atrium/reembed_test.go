package main

import (
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
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const bigModel = "wide-embedder"

type fakeEmbedder struct {
	*httptest.Server
	mu     sync.Mutex
	allow  int
	texts  map[string]int
	models []string
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
		f.texts[req.Model] += len(req.Input)
		f.models = append(f.models, req.Model)
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

func (f *fakeEmbedder) lastModel() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.models) == 0 {
		return ""
	}
	return f.models[len(f.models)-1]
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

func TestChangingTheEmbedderReembedsInTheBackgroundAndSwapsWhenComplete(t *testing.T) {
	fake := newFakeEmbedder(t)
	dir, _ := moduleFixture(t, flagsExcept("", ""))
	data, err := os.ReadFile(filepath.Join(golden.Dir(), "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "atrium.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(settingEnv(mail.KeyEmbedURL), fake.URL+"/v1")
	t.Setenv(settingEnv(mail.KeyEmbedModel), "embeddinggemma")
	t.Setenv(settingEnv(mail.KeyEmbedBatch), "40")
	search := func() string {
		t.Helper()
		got := runApp("notion", "search", "project notes")
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
	if after := search(); after != before || fake.lastModel() != "embeddinggemma" {
		t.Fatalf("search with an unchanged embedder: %q, was %q (model %q)", after, before, fake.lastModel())
	}

	t.Setenv(settingEnv(mail.KeyEmbedModel), bigModel)
	fake.release(1)
	base, _, stop = startServeStoppable(t, dir)
	var first, total int
	waitFor(t, func() bool {
		first, total, _ = embedProgress(t, base)
		return first > 0
	})
	if during := search(); during != before || fake.lastModel() != "embeddinggemma" {
		t.Fatalf("search while re-embedding: %q, was %q (query model %q)", during, before, fake.lastModel())
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
		"(SELECT COUNT(*) FROM embed_index), (SELECT COUNT(*) FROM sqlite_master WHERE name IN ('message_vec', 'notion_vec'))").
		Scan(&rows, &generations, &oldTables); err != nil {
		t.Fatal(err)
	}
	if rows != total || generations != 1 || oldTables != 0 {
		t.Fatalf("after the swap: %d vectors for %d documents, %d index rows, %d old tables", rows, total, generations, oldTables)
	}
	if sent := fake.embedded(bigModel); sent > total+40+8 {
		t.Errorf("the restart re-embedded finished rows: %d texts sent for %d documents", sent, total)
	}
	if after := search(); after == "" || fake.lastModel() != bigModel {
		t.Errorf("search after the swap: %q (query model %q)", after, fake.lastModel())
	}
}
