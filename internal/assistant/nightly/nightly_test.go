package nightly_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/nightly"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/py"
)

const tz = "America/Port_of_Spain"

func local(y int, mo time.Month, d, h, mi int) float64 {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		panic(err)
	}
	return float64(time.Date(y, mo, d, h, mi, 0, 0, loc).Unix())
}

var now = local(2026, 10, 15, 12, 0)

func TestOnlyNightlyOffersThreadsToday(t *testing.T) {
	tools := nightly.Tools()
	if len(tools) != 1 || tools[0].Name != "threads_today" || tools[0].Effect != tool.Read ||
		len(tools[0].Contexts) != 1 || tools[0].Contexts[0] != tool.Nightly {
		t.Fatalf("Tools = %+v", tools)
	}
}

func TestThreadsTodayReadsTheWindow(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	mk := func(kind, title string) int64 {
		id, err := service.CreateThread(ctx, d.Write, now, threads.NewThread{Kind: kind, Title: &title})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	say := func(thread int64, role, text string, at float64) {
		block, _ := json.Marshal(map[string]string{"type": "text", "text": text})
		if _, err := journal.AppendMessage(ctx, d, thread, nil, role, []json.RawMessage{block}, at, nil); err != nil {
			t.Fatal(err)
		}
	}
	mk("chat", "old")
	recent, old, cron := mk("chat", "recent"), mk("chat", "older"), mk("cron", "cron")
	say(recent, journal.User, "Plan the trip "+strings.Repeat("x", 5000), now-3600)
	say(old, journal.User, "ancient", now-3*86400)
	say(cron, journal.Assistant, "report", now-60)
	tc := &tool.Context{DB: d, Env: config.Map(map[string]string{"ATRIUM_TZ": tz}), Kind: tool.Nightly, Now: func() float64 { return now }}
	handler := nightly.Tools()[0].Handler
	ids := func(args py.Dict) map[int64]bool {
		out, err := handler(ctx, tc, args)
		if err != nil {
			t.Fatal(err)
		}
		dict := out.(py.Dict)
		if v, _ := dict.Get("untrusted"); v != true {
			t.Errorf("untrusted = %v", v)
		}
		list, _ := dict.Get("threads")
		got := map[int64]bool{}
		for _, item := range list.([]any) {
			row := item.(py.Dict)
			id, _ := row.Get("thread_id")
			text, _ := row.Get("text")
			if len([]rune(text.(string))) > nightly.ThreadChars {
				t.Errorf("thread %v text has %d characters", id, len([]rune(text.(string))))
			}
			got[id.(int64)] = true
		}
		return got
	}
	if got := ids(py.Dict{}); len(got) != 2 || !got[recent] || !got[cron] {
		t.Errorf("last 24 hours = %v", got)
	}
	if got := ids(py.Dict{{Key: "date", Value: "2026-10-12"}}); len(got) != 1 || !got[old] {
		t.Errorf("2026-10-12 = %v", got)
	}
	_, err := handler(ctx, tc, py.Dict{{Key: "date", Value: "not-a-date"}})
	var te tool.Error
	if !errors.As(err, &te) || te != "date must be YYYY-MM-DD" {
		t.Errorf("bad date error = %v", err)
	}
}

func TestWindowIsTheLocalDay(t *testing.T) {
	loc, _ := time.LoadLocation(tz)
	date := "2026-10-12"
	start, end, err := nightly.Window(&date, loc, now)
	if err != nil || start != local(2026, 10, 12, 0, 0) || end != local(2026, 10, 13, 0, 0) {
		t.Fatalf("Window = %v, %v, %v", start, end, err)
	}
	start, end, _ = nightly.Window(nil, loc, now)
	if start != now-86400 || end != now {
		t.Errorf("default window = %v, %v", start, end)
	}
}
