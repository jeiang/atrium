package db_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

type vec016 struct {
	SQLite       string                       `json:"sqlite"`
	SQLiteVec    string                       `json:"sqlite_vec"`
	QuickCheck   string                       `json:"quick_check"`
	ShadowTables map[string]string            `json:"shadow_tables"`
	Embeddings   map[string]map[string]string `json:"embeddings"`
	Partitions   map[string]map[string]string `json:"partitions"`
	Queries      []vec016Query                `json:"queries"`
}

type vec016Hit struct {
	Rowid    int64   `json:"rowid"`
	Distance float64 `json:"distance"`
}

type vec016Query struct {
	Text          string      `json:"text"`
	Vector        string      `json:"vector"`
	Message       []vec016Hit `json:"message_vec"`
	MessageICloud []vec016Hit `json:"message_vec_icloud"`
	MessageGmail  []vec016Hit `json:"message_vec_gmail"`
	MessageExact  []vec016Hit `json:"message_vec_exact"`
	NotionPublic  []vec016Hit `json:"notion_vec_public"`
	NotionPrivate []vec016Hit `json:"notion_vec_private"`
}

const (
	messageKNN = "SELECT rowid, distance FROM message_vec WHERE embedding MATCH ? AND k = ?"
	notionKNN  = "SELECT rowid, distance FROM notion_vec WHERE embedding MATCH ? AND k = ? AND visibility = ?"
	exactKNN   = "SELECT rowid, vec_distance_cosine(embedding, ?) AS d FROM message_vec ORDER BY d, rowid"
)

func hits(t *testing.T, q *sql.DB, query string, args ...any) []vec016Hit {
	t.Helper()
	rows, err := q.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []vec016Hit
	for rows.Next() {
		var h vec016Hit
		if err := rows.Scan(&h.Rowid, &h.Distance); err != nil {
			t.Fatal(err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func vecShadowTables(t *testing.T, q *sql.DB) map[string]string {
	t.Helper()
	rows, err := q.Query("SELECT name, sql FROM sqlite_master WHERE type = 'table' AND sql IS NOT NULL AND (name LIKE 'message\\_vec\\_%' ESCAPE '\\' OR name LIKE 'notion\\_vec\\_%' ESCAPE '\\')")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var name, text string
		if err := rows.Scan(&name, &text); err != nil {
			t.Fatal(err)
		}
		out[name] = text
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func copyFixture(t *testing.T, src string) string {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "atrium.db")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestVec016DB(t *testing.T) {
	var rec vec016
	readJSON(t, "atrium-vec016.json", &rec)
	if rec.SQLite != "3.51.2" || rec.SQLiteVec != "v0.1.6" || rec.QuickCheck != "ok" {
		t.Fatalf("fixture written by SQLite %s, sqlite-vec %s (quick_check %s); want 3.51.2, v0.1.6", rec.SQLite, rec.SQLiteVec, rec.QuickCheck)
	}
	path := copyFixture(t, filepath.Join(golden.Dir(), "atrium-vec016.db"))

	raw := db.Open(path)
	before := vecShadowTables(t, raw)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(before, rec.ShadowTables) {
		t.Fatalf("fixture shadow tables %v, recorded %v", slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(rec.ShadowTables)))
	}

	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(t.Context(), d, path); err != nil {
		t.Fatalf("migration 0001 rejected the sqlite-vec 0.1.6 database: %v", err)
	}
	var vecVersion string
	if err := d.Read.QueryRow("SELECT vec_version()").Scan(&vecVersion); err != nil {
		t.Fatal(err)
	}
	if vecVersion != "v0.1.9" {
		t.Fatalf("vec_version() = %s, want v0.1.9", vecVersion)
	}
	var versions []int
	rows, err := d.Read.Query("SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if !slices.Equal(versions, []int{1}) {
		t.Errorf("schema_migrations versions %v, want [1]", versions)
	}
	quickCheck(t, d)

	after := vecShadowTables(t, d.Read)
	if !maps.Equal(after, rec.ShadowTables) {
		t.Errorf("migration changed the vec0 shadow tables:\nbefore %v\nafter  %v", rec.ShadowTables, after)
	}
	if fresh := vecShadowTables(t, dbtest.Schema(t).Read); !maps.Equal(fresh, rec.ShadowTables) {
		t.Errorf("sqlite-vec 0.1.9 shadow tables differ from 0.1.6:\n0.1.9 %v\n0.1.6 %v", fresh, rec.ShadowTables)
	}
	for _, table := range []string{"message_vec", "notion_vec"} {
		var text string
		if err := d.Read.QueryRow("SELECT sql FROM sqlite_master WHERE name = ?", table).Scan(&text); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.ToUpper(text), "USING VEC0") {
			t.Errorf("%s is not a vec0 table: %s", table, text)
		}
	}

	for _, table := range []string{"message_vec", "notion_vec"} {
		part := map[string]string{"message_vec": "account", "notion_vec": "visibility"}[table]
		rows, err := d.Read.Query("SELECT rowid, " + part + ", embedding FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		got, gotPart := map[string]string{}, map[string]string{}
		for rows.Next() {
			var id int64
			var p string
			var blob []byte
			if err := rows.Scan(&id, &p, &blob); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(blob)
			got[strconv.FormatInt(id, 10)] = hex.EncodeToString(sum[:])
			gotPart[strconv.FormatInt(id, 10)] = p
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		if !maps.Equal(got, rec.Embeddings[table]) || !maps.Equal(gotPart, rec.Partitions[table]) {
			t.Errorf("%s rows differ from the 0.1.6 record:\ngot  %v %v\nwant %v %v", table, got, gotPart, rec.Embeddings[table], rec.Partitions[table])
		}
	}

	for _, q := range rec.Queries {
		blob, err := hex.DecodeString(q.Vector)
		if err != nil {
			t.Fatal(err)
		}
		sameHits(t, q.Text+" message_vec", hits(t, d.Read, messageKNN, blob, 4), q.Message)
		sameHits(t, q.Text+" message_vec icloud", hits(t, d.Read, messageKNN+" AND account = ?", blob, 2, "icloud"), q.MessageICloud)
		sameHits(t, q.Text+" message_vec gmail", hits(t, d.Read, messageKNN+" AND account = ?", blob, 3, "gmail"), q.MessageGmail)
		sameHits(t, q.Text+" message_vec exact", hits(t, d.Read, exactKNN, blob), q.MessageExact)
		sameHits(t, q.Text+" notion_vec public", hits(t, d.Read, notionKNN, blob, 2, "public"), q.NotionPublic)
		sameHits(t, q.Text+" notion_vec private", hits(t, d.Read, notionKNN, blob, 2, "private"), q.NotionPrivate)
	}

	q := rec.Queries[0]
	blob, err := hex.DecodeString(q.Vector)
	if err != nil {
		t.Fatal(err)
	}
	const added, removed = int64(7), int64(2)
	if _, err := d.Write.Exec("INSERT INTO message_vec (rowid, account, embedding) VALUES (?, 'gmail', ?)", added, blob); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("DELETE FROM message_vec WHERE rowid = ?", removed); err != nil {
		t.Fatal(err)
	}
	var want []vec016Hit
	for _, h := range q.MessageExact {
		if h.Rowid != removed {
			want = append(want, h)
		}
	}
	got := hits(t, d.Read, messageKNN, blob, len(want)+1)
	if len(got) != len(want)+1 || got[0].Rowid != added || got[0].Distance > 1e-6 {
		t.Fatalf("KNN after insert+delete = %v, want rowid %d first at distance 0 then %v", got, added, want)
	}
	sameHits(t, "message_vec after insert+delete", got[1:], want)
	var icloud []vec016Hit
	for _, h := range q.MessageExact {
		if h.Rowid != removed && rec.Partitions["message_vec"][strconv.FormatInt(h.Rowid, 10)] == "icloud" {
			icloud = append(icloud, h)
		}
	}
	sameHits(t, "message_vec icloud after delete", hits(t, d.Read, messageKNN+" AND account = ?", blob, 3, "icloud"), icloud)
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM message_vec WHERE rowid = ?", removed).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("rowid %d still in message_vec after DELETE", removed)
	}
	quickCheck(t, d)
}

func sameHits(t *testing.T, label string, got, want []vec016Hit) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\ngot  %v\nwant %v", label, got, want)
	}
}
