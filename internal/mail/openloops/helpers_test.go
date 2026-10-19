package openloops_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail/openloops"
)

const tz = "America/Port_of_Spain"

var (
	now = time.Date(2026, 10, 15, 12, 0, 0, 0, mustZone(tz)).Unix()
	ctx = context.Background()
)

func mustZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func NOW() float64 { return float64(now) }

func ago(days float64) float64 { return NOW() - days*openloops.Day }

func env() config.Env { return config.Map(map[string]string{"ATRIUM_TZ": tz}) }

type fields map[string]any

func newDB(t *testing.T) *db.DB { return dbtest.Mail(t) }

func mail(t *testing.T, d *db.DB, uid int, days float64, f fields) int64 {
	t.Helper()
	row := fields{"seen": 1, "date_ts": ago(days), "from_addr": "pat@person.test", "subject": "Hello", "msgid": fmt.Sprintf("m%d@x.test", uid)}
	for k, v := range f {
		row[k] = v
	}
	if to, ok := row["to_addrs"].(string); ok {
		row["to_addrs"] = []string{to}
	}
	return dbtest.AddMessage(t, d, uid, "", row)
}

func sent(t *testing.T, d *db.DB, uid int, days float64, to string, f fields) int64 {
	t.Helper()
	row := fields{"sent": 1, "from_addr": "me@mine.test", "to_addrs": []string{to}}
	for k, v := range f {
		row[k] = v
	}
	return mail(t, d, uid, days, row)
}

func exec(t *testing.T, d *db.DB, query string, args ...any) {
	t.Helper()
	if _, err := d.Write.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, d *db.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := d.Read.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type state struct{ state, counterpart string }

func states(t *testing.T, d *db.DB) map[string]state {
	t.Helper()
	rows, err := d.Read.Query("SELECT thread_key, state, counterpart FROM open_loops")
	if err != nil {
		t.Fatal(err)
	}
	out, err := db.Collect(rows, func(r *sql.Rows) ([3]string, error) {
		var v [3]string
		return v, r.Scan(&v[0], &v[1], &v[2])
	})
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]state{}
	for _, v := range out {
		m[v[0]] = state{v[1], v[2]}
	}
	return m
}

func stateList(t *testing.T, d *db.DB) []string {
	t.Helper()
	var out []string
	for _, s := range states(t, d) {
		out = append(out, s.state)
	}
	return out
}

func compute(t *testing.T, d *db.DB, at float64) openloops.Counts {
	t.Helper()
	counts, err := openloops.Compute(ctx, d, at)
	if err != nil {
		t.Fatal(err)
	}
	return counts
}
