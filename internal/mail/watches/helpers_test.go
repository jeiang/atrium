package watches_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/py"
)

const tz = "America/Port_of_Spain"

var ctx = context.Background()

func mustZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

var now = float64(time.Date(2026, 10, 15, 12, 0, 0, 0, mustZone(tz)).Unix())

func env() config.Env { return config.Map(map[string]string{"ATRIUM_TZ": tz}) }

type fields map[string]any

func mail(t *testing.T, d *db.DB, uid int, daysAgo float64, body string, f fields) int64 {
	t.Helper()
	row := fields{"from_addr": "billing@shop.test", "from_domain": "shop.test", "date_ts": now - daysAgo*watches.Day}
	for k, v := range f {
		row[k] = v
	}
	return dbtest.AddMessage(t, d, uid, body, row)
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

// chat is fake_chat: it records each call and answers with answer.
type chat struct {
	answer any
	calls  []string
	err    error
}

func (c *chat) call(_ context.Context, _, _, user string, _ any, _ int) (any, error) {
	c.calls = append(c.calls, user)
	return c.answer, c.err
}

func opts(c *chat) watches.Options {
	return watches.Options{Chat: c.call, Clock: func() float64 { return now }, Env: env()}
}

func scan(t *testing.T, d *db.DB, c *chat) watches.ScanResult {
	t.Helper()
	got, err := watches.ScanPackages(ctx, d, config.Config{}, watches.ScanLimit, now, opts(c))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func answer(pairs ...any) py.Dict {
	var d py.Dict
	for i := 0; i < len(pairs); i += 2 {
		d = append(d, py.Pair{Key: pairs[i].(string), Value: pairs[i+1]})
	}
	return d
}

func order(merchant, ref, tracking, status string) py.Dict {
	return answer("is_order", true, "carrier", "", "tracking_number", tracking, "merchant", merchant, "order_ref", ref,
		"status", status, "status_text", "Order confirmed")
}

type pkg struct {
	carrier, tracking, merchant, orderRef, status string
	source, last                                  int64
}

func packages(t *testing.T, d *db.DB) []pkg {
	t.Helper()
	rows, err := d.Read.Query("SELECT carrier, tracking_number, merchant, order_ref, status, source_message_id, last_message_id FROM packages ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []pkg
	for rows.Next() {
		var p pkg
		if err := rows.Scan(&p.carrier, &p.tracking, &p.merchant, &p.orderRef, &p.status, &p.source, &p.last); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}
