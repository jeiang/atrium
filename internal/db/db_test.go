package db

import (
	"database/sql"
	"encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/golden.json")

const atriumDBSQLiteVersion = "3.51.2"

const vecVersion = "v0.1.9"

const embedDim = 768

func openTest(t *testing.T) *sql.DB {
	t.Helper()
	db := Open(filepath.Join(t.TempDir(), "atrium.db"), "PRAGMA busy_timeout = 30000", "PRAGMA foreign_keys = ON")
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		t.Fatal(err)
	}
	return db
}

func versionAtLeast(got, want string) bool {
	g, w := strings.Split(got, "."), strings.Split(want, ".")
	for i := range max(len(g), len(w)) {
		var a, b int
		if i < len(g) {
			a, _ = strconv.Atoi(g[i])
		}
		if i < len(w) {
			b, _ = strconv.Atoi(w[i])
		}
		if a != b {
			return a > b
		}
	}
	return true
}

func TestVersionAtLeast(t *testing.T) {
	for _, c := range []struct {
		got  string
		want bool
	}{
		{"3.51.2", true},
		{"3.51.10", true},
		{"3.53.4", true},
		{"4.0", true},
		{"3.51.1", false},
		{"3.50.9", false},
		{"3.9.0", false},
	} {
		if got := versionAtLeast(c.got, atriumDBSQLiteVersion); got != c.want {
			t.Errorf("versionAtLeast(%q, %q) = %v, want %v", c.got, atriumDBSQLiteVersion, got, c.want)
		}
	}
}

func TestBundledSQLite(t *testing.T) {
	db := openTest(t)
	var version, vec string
	if err := db.QueryRow("SELECT sqlite_version(), vec_version()").Scan(&version, &vec); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: SQLite %s, sqlite-vec %s", driverName, version, vec)
	if !versionAtLeast(version, atriumDBSQLiteVersion) {
		t.Errorf("bundled SQLite %s is older than %s, which wrote atrium.db", version, atriumDBSQLiteVersion)
	}
	if vec != vecVersion {
		t.Errorf("vec_version() = %s, want %s", vec, vecVersion)
	}
	var fts5 bool
	if err := db.QueryRow("SELECT sqlite_compileoption_used('ENABLE_FTS5')").Scan(&fts5); err != nil {
		t.Fatal(err)
	}
	if !fts5 {
		t.Error("SQLite is built without FTS5")
	}
}

func TestPragmasPerConnection(t *testing.T) {
	db := openTest(t)
	db.SetMaxOpenConns(3)
	conns := make([]*sql.Conn, 3)
	for i := range conns {
		c, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		conns[i] = c
	}
	for i, c := range conns {
		var fk, busy int
		var mode string
		if err := c.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if fk != 1 || busy != 30000 || mode != "wal" {
			t.Errorf("conn %d: foreign_keys=%d busy_timeout=%d journal_mode=%s", i, fk, busy, mode)
		}
	}
}

type knnRow struct {
	Rowid    int64  `json:"rowid"`
	Distance uint32 `json:"distance_bits"`
}

type ftsRow struct {
	Rowid   int64   `json:"rowid"`
	BM25    float64 `json:"bm25"`
	Snippet string  `json:"snippet"`
}

type golden struct {
	KNN          []knnRow `json:"knn"`
	KNNPartition []knnRow `json:"knn_partition"`
	Exact        []knnRow `json:"exact"`
	FTS          []ftsRow `json:"fts"`
}

func vector(seed uint32) []float32 {
	v := make([]float32, embedDim)
	s := seed*2654435761 + 1
	for i := range v {
		s ^= s << 13
		s ^= s >> 17
		s ^= s << 5
		v[i] = float32(s)/float32(math.MaxUint32)*2 - 1
	}
	return v
}

func cosineDistance(a, b []float32) float64 {
	var dot, am, bm float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		am += float64(a[i]) * float64(a[i])
		bm += float64(b[i]) * float64(b[i])
	}
	return 1 - dot/(math.Sqrt(am)*math.Sqrt(bm))
}

func queryKNN(t *testing.T, db *sql.DB, query string, args ...any) []knnRow {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []knnRow
	for rows.Next() {
		var r knnRow
		var d float64
		if err := rows.Scan(&r.Rowid, &d); err != nil {
			t.Fatal(err)
		}
		r.Distance = math.Float32bits(float32(d))
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func vecFixture(t *testing.T, db *sql.DB) map[int64][]float32 {
	t.Helper()
	if _, err := db.Exec("CREATE VIRTUAL TABLE message_vec USING vec0(account TEXT PARTITION KEY, embedding FLOAT[768] DISTANCE_METRIC=cosine)"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	vecs := map[int64][]float32{}
	for id := int64(1); id <= 200; id++ {
		account := "icloud"
		if id%2 == 0 {
			account = "gmail"
		}
		v := vector(uint32(id))
		vecs[id] = v
		if _, err := tx.Exec("DELETE FROM message_vec WHERE rowid = ?", id); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec("INSERT INTO message_vec(rowid, account, embedding) VALUES (?, ?, ?)", id, account, SerializeFloat32(v)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return vecs
}

func ftsFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec("CREATE VIRTUAL TABLE message_fts USING fts5(subject, sender, body)"); err != nil {
		t.Fatal(err)
	}
	docs := []struct{ subject, sender, body string }{
		{"Invoice for October", "billing@example.com", "Your invoice is attached. Payment due in thirty days."},
		{"Lunch on Friday?", "sam@example.com", "Are you free for lunch on Friday near the office?"},
		{"Re: invoice question", "ana@example.com", "The invoice total looks wrong; the invoice lists two seats."},
		{"Flight confirmation", "airline@example.com", "Your flight to Lisbon is confirmed. Seat 14A."},
		{"Weekly newsletter", "news@example.com", "Payment apps, invoices and other news this week."},
	}
	for i, d := range docs {
		if _, err := db.Exec("INSERT INTO message_fts(rowid, subject, sender, body) VALUES (?, ?, ?, ?)", i+1, d.subject, d.sender, d.body); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO message_fts(message_fts) VALUES ('integrity-check')"); err != nil {
		t.Fatal(err)
	}
}

func queryFTS(t *testing.T, db *sql.DB) []ftsRow {
	t.Helper()
	rows, err := db.Query(`SELECT rowid, bm25(message_fts), snippet(message_fts, 2, '[', ']', '…', 8)
		FROM message_fts WHERE message_fts MATCH ? ORDER BY bm25(message_fts) LIMIT 50`, "invoice OR payment")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []ftsRow
	for rows.Next() {
		var r ftsRow
		if err := rows.Scan(&r.Rowid, &r.BM25, &r.Snippet); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestVecAndFTS5(t *testing.T) {
	db := openTest(t)
	vecs := vecFixture(t, db)
	ftsFixture(t, db)
	q := vector(10_000)
	qb := SerializeFloat32(q)

	got := golden{
		KNN:          queryKNN(t, db, "SELECT rowid, distance FROM message_vec WHERE embedding MATCH ? AND k = ?", qb, 10),
		KNNPartition: queryKNN(t, db, "SELECT rowid, distance FROM message_vec WHERE embedding MATCH ? AND k = ? AND account = ?", qb, 10, "gmail"),
		Exact:        queryKNN(t, db, "SELECT rowid, vec_distance_cosine(embedding, ?) AS d FROM message_vec WHERE account = ? ORDER BY d LIMIT 10", qb, "gmail"),
		FTS:          queryFTS(t, db),
	}

	if len(got.KNN) != 10 || len(got.KNNPartition) != 10 || len(got.FTS) != 3 {
		t.Fatalf("result sizes: knn=%d partition=%d fts=%d", len(got.KNN), len(got.KNNPartition), len(got.FTS))
	}
	for _, r := range append(got.KNN, got.KNNPartition...) {
		want := cosineDistance(q, vecs[r.Rowid])
		if d := float64(math.Float32frombits(r.Distance)); math.Abs(d-want) > 1e-5 {
			t.Errorf("rowid %d: distance %v, float64 reference %v", r.Rowid, d, want)
		}
	}
	for _, r := range got.KNNPartition {
		if r.Rowid%2 != 0 {
			t.Errorf("partition query returned icloud rowid %d", r.Rowid)
		}
	}
	for i := range got.Exact {
		if got.Exact[i] != got.KNNPartition[i] {
			t.Errorf("exact[%d] = %+v, knn = %+v", i, got.Exact[i], got.KNNPartition[i])
		}
	}
	for _, r := range got.FTS {
		if !strings.Contains(strings.ToLower(r.Snippet), "[invoice") && !strings.Contains(strings.ToLower(r.Snippet), "[payment") {
			t.Errorf("rowid %d: snippet %q lacks a highlighted match", r.Rowid, r.Snippet)
		}
		if r.BM25 >= 0 {
			t.Errorf("rowid %d: bm25 %v, want negative", r.Rowid, r.BM25)
		}
	}

	path := filepath.Join("testdata", "golden.json")
	if *update {
		b, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want golden
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	diff(t, "knn", got.KNN, want.KNN)
	diff(t, "knn_partition", got.KNNPartition, want.KNNPartition)
	diff(t, "exact", got.Exact, want.Exact)
	if len(got.FTS) != len(want.FTS) {
		t.Fatalf("%s fts: %d rows, golden has %d", driverName, len(got.FTS), len(want.FTS))
	}
	for i, g := range got.FTS {
		w := want.FTS[i]
		if g.Rowid != w.Rowid || g.Snippet != w.Snippet || math.Abs(g.BM25-w.BM25) > 1e-12*math.Abs(w.BM25) {
			t.Errorf("%s fts[%d] = %+v, golden %+v", driverName, i, g, w)
		}
	}
}

func diff[T comparable](t *testing.T, name string, got, want []T) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s %s: %d rows, golden has %d", driverName, name, len(got), len(want))
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s %s[%d] = %+v, golden %+v", driverName, name, i, got[i], want[i])
		}
	}
}

func TestCollectIsNeverNil(t *testing.T) {
	d := openTest(t)
	rows, err := d.QueryContext(t.Context(), "SELECT 1 WHERE 0")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Collect(rows, func(r *sql.Rows) (int, error) { return 0, nil })
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("Collect = %v, %v", got, err)
	}
}
