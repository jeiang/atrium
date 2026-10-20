package mailtools_test

import (
	"fmt"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/py"
)

const day = 86400.0

func ago(days float64) float64 { return now - days*day }

func loopMail(t *testing.T, d *db.DB, uid int, days float64, f fields) int64 {
	t.Helper()
	row := fields{"seen": 1, "date_ts": ago(days), "from_addr": "pat@person.test", "subject": "Hello", "msgid": fmt.Sprintf("m%d@x.test", uid)}
	for k, v := range f {
		row[k] = v
	}
	return dbtest.AddMessage(t, d, uid, "", row)
}

func loopSent(t *testing.T, d *db.DB, uid int, days float64, to string, f fields) int64 {
	t.Helper()
	row := fields{"sent": 1, "from_addr": "me@mine.test", "to_addrs": []string{to}}
	for k, v := range f {
		row[k] = v
	}
	return loopMail(t, d, uid, days, row)
}

func compute(t *testing.T, d *db.DB, at float64) {
	t.Helper()
	if _, err := openloops.Compute(t.Context(), d, at); err != nil {
		t.Fatal(err)
	}
}

func TestOpenLoopsAndCommitmentsTools(t *testing.T) {
	d := newDB(t)
	loopMail(t, d, 1, 2, nil)
	loopSent(t, d, 2, 9, "lee@other.test", fields{"subject": "Pending", "in_reply_to": ""})
	compute(t, d, now)
	exec(t, d, "INSERT INTO commitments (message_id, text, owner, due, created_ts) VALUES (1, 'Call back', 'me', '2026-10-20', ?)", now)
	tc := ctxFor(d, config.Config{}, map[string]string{"ATRIUM_TZ": tz})

	both := list(t, get(t, mustCall(t, tc, "open_loops", `{}`), "loops"))
	states := map[string]bool{}
	for _, l := range both {
		states[get(t, l.(py.Dict), "state").(string)] = true
	}
	if len(both) != 2 || !states["owe_reply"] || !states["waiting_on_them"] {
		t.Errorf("loops = %s", py.JSONDumps(both))
	}
	only := list(t, get(t, mustCall(t, tc, "open_loops", `{"state": "waiting_on_them", "limit": 5}`), "loops"))
	if len(only) != 1 || get(t, only[0].(py.Dict), "counterpart") != "lee@other.test" {
		t.Errorf("only = %s", py.JSONDumps(only))
	}
	row := only[0].(py.Dict)
	var keys string
	for _, p := range row {
		keys += p.Key + ","
	}
	if keys != "state,message_id,counterpart,subject,days_open,since,account," || get(t, row, "days_open") != int64(9) {
		t.Errorf("row = %s", py.JSONDumps(row))
	}
	if note := get(t, mustCall(t, tc, "open_loops", `{}`), "note"); note !=
		"owe_reply: the last message in the thread is theirs and awaits the operator; waiting_on_them: the operator wrote last." {
		t.Errorf("note = %v", note)
	}
	open := list(t, get(t, mustCall(t, tc, "commitments", `{"status": "open"}`), "commitments"))
	if len(open) != 1 || get(t, open[0].(py.Dict), "text") != "Call back" || get(t, open[0].(py.Dict), "due") != "2026-10-20" ||
		get(t, open[0].(py.Dict), "reminder_proposal_id") != nil {
		t.Errorf("open = %s", py.JSONDumps(open))
	}
	if done := list(t, get(t, mustCall(t, tc, "commitments", `{"status": "done"}`), "commitments")); len(done) != 0 {
		t.Errorf("done = %v", done)
	}
	_, err := call(t, tc, "open_loops", `{"state": "bogus"}`)
	wantToolError(t, err, "state must be one of owe_reply, waiting_on_them")
	_, err = call(t, tc, "commitments", `{"status": "bogus"}`)
	wantToolError(t, err, "status must be one of open, done, dismissed")
}

func TestOpenLoopsHidesSnoozedAndDismissedAndFollowsTZ(t *testing.T) {
	d := newDB(t)
	moment := 1793476800.0
	loopMail(t, d, 1, 0, fields{"date_ts": moment})
	compute(t, d, moment+day)
	tc := ctxFor(d, config.Config{}, map[string]string{"ATRIUM_TZ": "Asia/Tokyo"})
	tc.Now = func() float64 { return moment + day }
	loops := list(t, get(t, mustCall(t, tc, "open_loops", `{}`), "loops"))
	if got := get(t, loops[0].(py.Dict), "since"); got != "2026-11-01T05:00" {
		t.Errorf("since = %v", got)
	}
	exec(t, d, "UPDATE open_loops SET dismissed = 1")
	if loops := list(t, get(t, mustCall(t, tc, "open_loops", `{}`), "loops")); len(loops) != 0 {
		t.Errorf("dismissed loop still listed: %v", loops)
	}
}

func TestOpenLoopsUnknownTimezoneIsNotAToolError(t *testing.T) {
	d := newDB(t)
	tc := ctxFor(d, config.Config{}, map[string]string{"ATRIUM_TZ": "Mars/Base"})
	_, err := call(t, tc, "open_loops", `{}`)
	var raised *tool.Raised
	if !asRaised(err, &raised) || raised.Class != "ScheduleError" {
		t.Errorf("err = %v", err)
	}
}

func TestOpenLoopsLimitIsBounded(t *testing.T) {
	d := newDB(t)
	for uid := 1; uid <= 3; uid++ {
		loopMail(t, d, uid, float64(uid), fields{"subject": fmt.Sprintf("S%d", uid), "from_addr": fmt.Sprintf("p%d@person.test", uid)})
	}
	compute(t, d, now)
	tc := ctxFor(d, config.Config{}, map[string]string{"ATRIUM_TZ": tz})
	for args, want := range map[string]int{`{"limit": 0}`: 1, `{"limit": -4}`: 1, `{"limit": 2}`: 2, `{"limit": 1000}`: 3, `{"limit": 2.9}`: 2} {
		if got := len(list(t, get(t, mustCall(t, tc, "open_loops", args), "loops"))); got != want {
			t.Errorf("%s: %d loops, want %d", args, got, want)
		}
	}
	_, err := call(t, tc, "open_loops", `{"limit": "5"}`)
	wantToolError(t, err, "limit must be a number")
}
