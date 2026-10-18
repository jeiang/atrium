package embed_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

func unit(i int) []float64 {
	v := make([]float64, config.EmbedDim)
	v[i] = 1
	return v
}

func mailDB(t *testing.T) *db.DB {
	t.Helper()
	d := dbtest.Schema(t)
	exec(t, d, "INSERT INTO accounts (name, kind) VALUES ('icloud', 'icloud')")
	exec(t, d, "INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (1, 'icloud', 'INBOX', 'INBOX', 'inbox')")
	return d
}

func exec(t *testing.T, d *db.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Write.ExecContext(t.Context(), q, args...); err != nil {
		t.Fatal(err)
	}
}

func addMessage(t *testing.T, d *db.DB, id int, subject, name, addr, body string) {
	t.Helper()
	exec(t, d, "INSERT INTO messages (id, account, mailbox_id, uidvalidity, uid, digest, subject, from_name, from_addr, first_seen) "+
		"VALUES (?, 'icloud', 1, 7, ?, ?, ?, ?, ?, 0)", id, id, fmt.Sprint(id), subject, name, addr)
	exec(t, d, "INSERT INTO message_fts (rowid, subject, sender, body) VALUES (?, ?, ?, ?)", id, subject, name+" "+addr, body)
}

func vector(t *testing.T, d *db.DB, table string, rowid int64) []byte {
	t.Helper()
	var blob []byte
	if err := d.Read.QueryRowContext(t.Context(), "SELECT embedding FROM "+table+" WHERE rowid = ?", rowid).Scan(&blob); err != nil {
		t.Fatalf("%s rowid %d: %v", table, rowid, err)
	}
	return blob
}

func count(t *testing.T, d *db.DB, q string) int {
	t.Helper()
	var n int
	if err := d.Read.QueryRowContext(t.Context(), q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestGoldenEmbeddingsMail(t *testing.T) {
	url := golden.Replay(t, golden.LoadScenario(t, "llm", "embeddings_mail").Exchanges).URL + "/v1"
	d := mailDB(t)
	addMessage(t, d, 1, "October invoice", "Billing", "billing@shop.test", "Your invoice for October is attached.")
	addMessage(t, d, 2, "", "", "x@y.test", "")
	n, err := embed.Pending(t.Context(), d, url)
	if err != nil || n != 2 {
		t.Fatalf("Pending = %d, %v", n, err)
	}
	for _, id := range []int64{1, 2} {
		if got := vector(t, d, "message_vec", id); !bytes.Equal(got, embed.Serialize(unit(1))) {
			t.Errorf("message %d vector differs", id)
		}
	}
	if count(t, d, "SELECT COUNT(*) FROM messages WHERE embedded = 1") != 2 {
		t.Error("messages not marked embedded")
	}
}

func TestGoldenEmbeddingsSearch(t *testing.T) {
	url := golden.Replay(t, golden.LoadScenario(t, "llm", "embeddings_search").Exchanges).URL + "/v1"
	got, err := embed.Texts(t.Context(), url, []string{embed.QueryText("where is my parcel")})
	if err != nil || len(got) != 1 || got[0][1] != 1 {
		t.Fatalf("Texts = %v", err)
	}
}

func TestGoldenEmbeddingsNotion(t *testing.T) {
	url := golden.Replay(t, golden.LoadScenario(t, "llm", "embeddings_notion").Exchanges).URL + "/v1"
	d := dbtest.Schema(t)
	exec(t, d, "INSERT INTO notion_pages (rid, id, title, markdown) VALUES (1, '00000000-0000-0000-0000-000000000001', 'Alpha plan', 'alpha roadmap and milestones')")
	exec(t, d, "INSERT INTO notion_pages (rid, id, title, in_trash) VALUES (2, 'trashed', 'Gone', 1)")
	n, err := embed.NotionPending(t.Context(), d, url, embed.Texts)
	if err != nil || n != 1 {
		t.Fatalf("NotionPending = %d, %v", n, err)
	}
	if got := vector(t, d, "notion_vec", 1); !bytes.Equal(got, embed.Serialize(unit(2))) {
		t.Error("notion vector differs")
	}
	if count(t, d, "SELECT COUNT(*) FROM notion_vec WHERE visibility = 'public'") != 1 || count(t, d, "SELECT embedded FROM notion_pages WHERE rid = 1") != 1 {
		t.Error("notion page not stored as public and embedded")
	}
}

func TestNotionPrivatePagesGoToPrivatePartition(t *testing.T) {
	d := dbtest.Schema(t)
	exec(t, d, "INSERT INTO notion_pages (rid, id, title, path, markdown, private) VALUES (5, 'p', '', 'Home', 'secret', 1)")
	var docs []string
	n, err := embed.NotionPending(t.Context(), d, "unused", func(_ context.Context, _ string, texts []string) ([][]float64, error) {
		docs = append(docs, texts...)
		return [][]float64{unit(3)}, nil
	})
	if err != nil || n != 1 || count(t, d, "SELECT COUNT(*) FROM notion_vec WHERE visibility = 'private'") != 1 {
		t.Fatalf("NotionPending = %d, %v", n, err)
	}
	if want := "title: none | text: Home\n\nsecret"; docs[0] != want {
		t.Errorf("document %q, want %q", docs[0], want)
	}
}

type embedServer struct {
	mu      sync.Mutex
	batches []int
	vectors func(text string) []float64
}

func (s *embedServer) start(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || r.URL.Path != "/v1/embeddings" || req.Model != embed.Model {
			t.Errorf("request %s %s: %v", r.URL.Path, req.Model, err)
		}
		s.mu.Lock()
		s.batches = append(s.batches, len(req.Input))
		s.mu.Unlock()
		type item struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		}
		data := make([]item, len(req.Input))
		for i, text := range req.Input {
			data[len(data)-1-i] = item{i, s.vectors(text)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

func TestPendingBatchesLiveMessages(t *testing.T) {
	d := mailDB(t)
	for id := 1; id <= 21; id++ {
		addMessage(t, d, id, fmt.Sprintf("s%d", id), "n", "a@b.test", "body")
	}
	exec(t, d, "UPDATE messages SET gone_at = 1 WHERE id = 21")
	s := &embedServer{vectors: func(text string) []float64 {
		var id int
		_, _ = fmt.Sscanf(text, "title: s%d", &id)
		return unit(id)
	}}
	n, err := embed.Pending(t.Context(), d, s.start(t))
	if err != nil || n != 20 || fmt.Sprint(s.batches) != "[16 4]" {
		t.Fatalf("Pending = %d, %v, batches %v", n, err, s.batches)
	}
	for id := int64(1); id <= 20; id++ {
		if !bytes.Equal(vector(t, d, "message_vec", id), embed.Serialize(unit(int(id)))) {
			t.Errorf("message %d got another message's vector", id)
		}
	}
	if count(t, d, "SELECT embedded FROM messages WHERE id = 21") != 0 {
		t.Error("gone message embedded")
	}
}

func TestPendingOrWarnDefersWhenEmbeddingsUnavailable(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	for _, c := range []struct{ url, warning string }{
		{"", "level=WARN msg=\"embedding unavailable: no service URL configured for '/embeddings'\"\n"},
		{closed.URL, "level=WARN msg=\"embedding unavailable: " + closed.URL + "/embeddings: <urlopen error "},
	} {
		d := mailDB(t)
		addMessage(t, d, 1, "s", "n", "a@b.test", "body")
		var buf bytes.Buffer
		passLog := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		}}))
		if err := embed.PendingOrWarn(t.Context(), d, c.url, passLog); err != nil {
			t.Fatalf("%q: %v", c.url, err)
		}
		if !strings.HasPrefix(buf.String(), c.warning) {
			t.Errorf("%q: log %q, want %q", c.url, buf.String(), c.warning)
		}
		if count(t, d, "SELECT embedded FROM messages WHERE id = 1") != 0 || count(t, d, "SELECT COUNT(*) FROM message_vec") != 0 {
			t.Errorf("%q: message embedded without a server", c.url)
		}
	}
}

func TestWrongShapePropagates(t *testing.T) {
	for _, c := range []struct {
		name    string
		vectors func(string) []float64
		reply   string
		kind    error
		msg     string
	}{
		{name: "dimension", vectors: func(string) []float64 { return make([]float64, 767) }, kind: httpx.ErrValue, msg: "embedding response shape mismatch: expected 1x768"},
		{name: "count", reply: `{"data": []}`, kind: httpx.ErrValue, msg: "embedding response shape mismatch: expected 1x768"},
		{name: "no data", reply: `{}`, kind: httpx.ErrKey},
		{name: "no embedding", reply: `{"data": [{"index": 0}]}`, kind: httpx.ErrKey},
		{name: "not json", reply: `nope`, kind: httpx.ErrValue},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := mailDB(t)
			addMessage(t, d, 1, "s", "n", "a@b.test", "body")
			var url string
			if c.vectors != nil {
				url = (&embedServer{vectors: c.vectors}).start(t)
			} else {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(c.reply)) }))
				t.Cleanup(srv.Close)
				url = srv.URL
			}
			err := embed.PendingOrWarn(t.Context(), d, url, slog.New(slog.DiscardHandler))
			var su *httpx.ServiceUnavailable
			if !errors.Is(err, c.kind) || errors.As(err, &su) || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("err = %v, want %v %q", err, c.kind, c.msg)
			}
			if count(t, d, "SELECT embedded FROM messages WHERE id = 1") != 0 {
				t.Error("message marked embedded")
			}
		})
	}
}

func TestDocumentTexts(t *testing.T) {
	body := strings.Repeat("ß", 1600)
	if got, want := embed.DocumentText("", "N", "a@b", body), "title: none | text: From: N <a@b>\n\n"+strings.Repeat("ß", 1500); got != want {
		t.Errorf("DocumentText = %q", got)
	}
	if got := embed.NotionDocumentText("T", "A / B", ""); got != "title: T | text: A / B\n\n" {
		t.Errorf("NotionDocumentText = %q", got)
	}
}

func TestNormalizeMatchesCPythonSum(t *testing.T) {
	v := []float64{1}
	for range 1000 {
		v = append(v, 1e-9)
	}
	got := embed.Normalize(v)
	if got[0] != 0.9999999999999996 || got[1] != 9.999999999999996e-10 {
		t.Errorf("Normalize = %v, %v", got[0], got[1])
	}
	tenths := make([]float64, config.EmbedDim)
	for i := range tenths {
		tenths[i] = 0.1
	}
	if got := embed.Normalize(tenths)[0]; got != 0.03608439182435161 {
		t.Errorf("Normalize(0.1...) = %v", got)
	}
	if got := embed.Normalize(make([]float64, 3)); got[0] != 0 {
		t.Errorf("zero vector = %v", got)
	}
}

func sparseEmbedding(text string) []float64 {
	v := make([]float64, config.EmbedDim)
	for _, token := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		sum := sha256.Sum256([]byte(token))
		v[binary.BigEndian.Uint32(sum[:4])%config.EmbedDim]++
	}
	return v
}

func TestReindexGoldenDBRoundTrip(t *testing.T) {
	d, _ := dbtest.Copy(t, filepath.Join(golden.Dir(), "atrium.db"))
	before := map[int64][]byte{}
	rows, err := d.Read.QueryContext(t.Context(), "SELECT v.rowid, v.embedding FROM message_vec v JOIN messages m ON m.id = v.rowid WHERE m.gone_at IS NULL")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			t.Fatal(err)
		}
		before[id] = blob
	}
	if err := rows.Close(); err != nil || len(before) == 0 {
		t.Fatalf("no golden vectors: %v", err)
	}
	live := count(t, d, "SELECT COUNT(*) FROM messages WHERE gone_at IS NULL")
	n, err := embed.Reindex(t.Context(), d, (&embedServer{vectors: sparseEmbedding}).start(t))
	if err != nil || n != live {
		t.Fatalf("Reindex = %d, %v; %d live messages", n, err, live)
	}
	if got := count(t, d, "SELECT COUNT(*) FROM message_vec"); got != live {
		t.Errorf("%d vectors after reindex, want %d", got, live)
	}
	if got := count(t, d, "SELECT COUNT(*) FROM messages WHERE embedded = 1"); got != live {
		t.Errorf("%d embedded after reindex, want %d", got, live)
	}
	for id, blob := range before {
		if !bytes.Equal(vector(t, d, "message_vec", id), blob) {
			t.Errorf("message %d: re-embedded vector differs from the golden DB", id)
		}
	}
	var account string
	var hits []int64
	q, err := d.Read.QueryContext(t.Context(), "SELECT v.rowid, v.account FROM message_vec v WHERE v.embedding MATCH ? AND k = 3 AND v.account = 'gmail' ORDER BY v.distance",
		embed.Serialize(embed.Normalize(sparseEmbedding(embed.QueryText("receipt")))))
	if err != nil {
		t.Fatal(err)
	}
	for q.Next() {
		var id int64
		if err := q.Scan(&id, &account); err != nil || account != "gmail" {
			t.Fatalf("knn row %d %q: %v", id, account, err)
		}
		hits = append(hits, id)
	}
	if err := q.Close(); err != nil || len(hits) != 3 {
		t.Errorf("knn after reindex: %v, %v", hits, err)
	}
}
