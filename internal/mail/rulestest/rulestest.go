// Package rulestest builds the databases of the routing tests: tests/helpers.py's prepare, add_message, filed and
// inbox over a migrated temp-file atrium.db.
package rulestest

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail/rules"
)

// Fields are the messages columns a test sets besides the defaults of add_message; list columns take []string.
type Fields map[string]any

// DB is a migrated database with both accounts and one `filed` mailbox each (ids 1 and 2).
func DB(t testing.TB) *db.DB {
	t.Helper()
	d := dbtest.Schema(t)
	for i, account := range []string{"icloud", "gmail"} {
		Exec(t, d, "INSERT OR IGNORE INTO accounts (name, kind) VALUES (?, ?)", account, account)
		Exec(t, d, "INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (?, ?, 'box', 'box', 'filed')", i+1, account)
	}
	return d
}

// Exec runs a write statement.
func Exec(t testing.TB, d *db.DB, query string, args ...any) {
	t.Helper()
	if _, err := d.Write.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// Unit is a vector with 1.0 at index and weight at other (when other >= 0).
func Unit(index int, other int, weight float32) []float32 {
	v := make([]float32, config.EmbedDim)
	v[index] = 1.0
	if other >= 0 {
		v[other] = weight
	}
	return v
}

// Vector is Unit(index) alone.
func Vector(index int) []float32 { return Unit(index, -1, 0) }

// Add is add_message: it inserts a message with its search row and, when vector is not nil, its embedding, and
// returns the message id.
func Add(t testing.TB, d *db.DB, uid int, vector []float32, body string, fields Fields) int64 {
	t.Helper()
	row := Fields{
		"account": "icloud", "mailbox_id": 1, "uidvalidity": 1, "digest": "d" + itoa(uid), "first_seen": 0.0,
		"uid": uid, "subject_tmpl": "",
	}
	for k, v := range fields {
		row[k] = v
	}
	for _, k := range []string{"to_addrs", "dkim_d", "esp", "att_types", "att_names"} {
		if list, ok := row[k].([]string); ok {
			b, _ := json.Marshal(list)
			row[k] = string(b)
		}
	}
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	columns, marks := "", ""
	args := make([]any, len(keys))
	for i, k := range keys {
		if i > 0 {
			columns += ", "
			marks += ", "
		}
		columns += k
		marks += "?"
		args[i] = row[k]
	}
	res, err := d.Write.ExecContext(t.Context(), "INSERT INTO messages ("+columns+") VALUES ("+marks+")", args...)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	subject, _ := row["subject"].(string)
	addr, _ := row["from_addr"].(string)
	Exec(t, d, "INSERT INTO message_fts (rowid, subject, sender, body) VALUES (?, ?, ?, ?)", id, subject, addr, body)
	if vector != nil {
		Exec(t, d, "INSERT INTO message_vec (rowid, account, embedding) VALUES (?, ?, ?)", id, row["account"], db.SerializeFloat32(vector))
		Exec(t, d, "UPDATE messages SET embedded = 1 WHERE id = ?", id)
	}
	return id
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// Filed is filed(): a message filed in folder.
func Filed(t testing.TB, d *db.DB, uid int, folder, addr, domain string, vector []float32, fields Fields) int64 {
	t.Helper()
	row := Fields{"from_addr": addr, "from_domain": domain, "folder": folder, "location": folder, "in_inbox": 0}
	for k, v := range fields {
		row[k] = v
	}
	return Add(t, d, uid, vector, "", row)
}

// Inbox is inbox(): a read, unflagged message in the inbox.
func Inbox(t testing.TB, d *db.DB, uid int, addr, domain string, vector []float32, fields Fields) int64 {
	t.Helper()
	row := Fields{"seen": 1, "flagged": 0, "in_inbox": 1, "location": "INBOX", "from_addr": addr, "from_domain": domain}
	for k, v := range fields {
		row[k] = v
	}
	return Add(t, d, uid, vector, "", row)
}

// Fetch is the messages row id as rules and the router read it.
func Fetch(t testing.TB, d *db.DB, id int64) rules.Message {
	t.Helper()
	m, err := rules.ScanMessage(d.Read.QueryRowContext(t.Context(), "SELECT "+rules.MessageColumns+" FROM messages WHERE id = ?", id))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Count runs a COUNT query.
func Count(t testing.TB, d *db.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := d.Read.QueryRowContext(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
