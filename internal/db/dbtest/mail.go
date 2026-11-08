package dbtest

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/db"
)

func AddMessage(t testing.TB, d *db.DB, uid int, body string, fields map[string]any) int64 {
	t.Helper()
	row := map[string]any{
		"account": "icloud", "mailbox_id": 1, "uidvalidity": 1, "digest": "", "first_seen": 0.0,
		"uid": uid, "subject_tmpl": "",
	}
	row["digest"] = fmt.Sprintf("d%d", uid)
	for k, v := range fields {
		if list, ok := v.([]string); ok {
			encoded, _ := json.Marshal(list)
			v = string(encoded)
		}
		row[k] = v
	}
	columns := make([]string, 0, len(row))
	for k := range row {
		columns = append(columns, k)
	}
	slices.Sort(columns)
	args := make([]any, len(columns))
	for i, k := range columns {
		args[i] = row[k]
	}
	res, err := d.Write.Exec("INSERT INTO messages ("+strings.Join(columns, ", ")+") VALUES ("+strings.TrimSuffix(strings.Repeat("?, ", len(columns)), ", ")+")", args...)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := row["subject"].(string)
	sender, _ := row["from_addr"].(string)
	if _, err := d.Write.Exec("INSERT INTO message_fts (rowid, subject, sender, body) VALUES (?, ?, ?, ?)", id, subject, sender, body); err != nil {
		t.Fatal(err)
	}
	return id
}
