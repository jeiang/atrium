package report

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/rulestest"
	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

func TestPatterns(t *testing.T) {
	rxtest.Run(t, map[string]*rx.Regexp{"src/atrium/report.py:SINCE": sinceRe})
}

func str(s string) *string { return &s }

func TestParseSince(t *testing.T) {
	env := config.Map(nil)
	for _, c := range []struct {
		since *string
		at    float64
		want  float64
	}{
		{nil, 0, 0},
		{str("2d"), 200000, 200000 - 2*86400},
		{str("3h"), 100000, 100000 - 3*3600},
		{str("7d\n"), 1e6, 1e6 - 7*86400},
		{str("٣d"), 1e6, 1e6 - 3*86400},
	} {
		got, err := ParseSince(env, c.since, time.Unix(int64(c.at), 0))
		if err != nil || got != c.want {
			t.Errorf("ParseSince(%v, %v) = %v, %v; want %v", c.since, c.at, got, err, c.want)
		}
	}
}

func TestParseSinceDateIsMidnightInAtriumTZ(t *testing.T) {
	env := config.Map(map[string]string{"ATRIUM_TZ": "America/Port_of_Spain"})
	for value, want := range map[string]float64{"2026-10-06": 1_791_259_200, "2026-1-5": 1_767_585_600, "2024-02-29": 1_709_179_200} {
		got, err := ParseSince(env, str(value), time.Unix(0, 0))
		if err != nil || got != want {
			t.Errorf("ParseSince(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
}

func TestParseSinceRejects(t *testing.T) {
	env := config.Map(nil)
	for _, value := range []string{"", "x", "2026-02-30", "2026-13-01", "2026-10-06 ", "2026-10-061", "0000-01-01", "7w", "h", "-3d", "٣w"} {
		if _, err := ParseSince(env, str(value), time.Unix(0, 0)); err == nil {
			t.Errorf("ParseSince(%q) succeeded", value)
		}
	}
}

func seedDecision(t *testing.T, d *db.DB, uid int, dest string, actual *string, msgid, domain, stage string) {
	t.Helper()
	mid := rulestest.Inbox(t, d, uid, fmt.Sprintf("s%d@%s", uid, domain), domain, nil, rulestest.Fields{"msgid": msgid, "digest": fmt.Sprintf("dg%d", uid)})
	switch {
	case actual == nil:
		rulestest.Exec(t, d, "UPDATE messages SET gone_at = 1 WHERE id = ?", mid)
	case *actual != "INBOX":
		rulestest.Exec(t, d, "UPDATE messages SET gone_at = 1 WHERE id = ?", mid)
		rulestest.Add(t, d, 500+uid, nil, "", rulestest.Fields{"digest": fmt.Sprintf("dg%d", uid), "location": *actual, "folder": *actual,
			"in_inbox": 0, "msgid": msgid})
	}
	rulestest.Exec(t, d, "INSERT INTO decisions (message_id, account, ts, stage, key, action, dest) VALUES (?, 'icloud', 10, ?, 'k', 'file', ?)",
		mid, stage, dest)
}

func TestShadowReportSummarizesAgreement(t *testing.T) {
	d := rulestest.DB(t)
	seedDecision(t, d, 1, "Bills", str("Bills"), "m1", "shop.test", "learned")
	seedDecision(t, d, 2, "Bills", str("Finance"), "m2", "bank.test", "learned")
	seedDecision(t, d, 3, "Travel", str("INBOX"), "m3", "shop.test", "learned")
	seedDecision(t, d, 4, "Misc", nil, "m4", "shop.test", "fallback")
	rulestest.Exec(t, d, "INSERT INTO flag_decisions VALUES (1, 'icloud', 10, 1, 1, 0, 0, 1), (2, 'icloud', 10, 0, 0, 0, 0, 1)")
	text, err := Build(t.Context(), d.Read, config.Map(nil), nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"== icloud ==", "== gmail ==", "decisions: 4", "learned=3", "fallback=1",
		"still in inbox=1, gone=1, moved=2, proposal equals actual=1 (50.0%)", "bank.test: Bills -> Finance",
		"contacts would-flag=1", "9B flag=2 of 2 judged", "both=1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "m1") || !strings.HasSuffix(text, "\n") || strings.HasSuffix(text, "\n\n") {
		t.Errorf("report text shape:\n%q", text)
	}
}

func TestShadowReportSinceFilters(t *testing.T) {
	d := rulestest.DB(t)
	seedDecision(t, d, 1, "Bills", str("Bills"), "m1", "shop.test", "learned")
	env := config.Map(nil)
	all, err := Build(t.Context(), d.Read, env, nil, time.Now())
	if err != nil || !strings.Contains(all, "decisions: 1") {
		t.Errorf("unfiltered: %v\n%s", err, all)
	}
	recent, err := Build(t.Context(), d.Read, env, str("1h"), time.Now())
	if err != nil || !strings.Contains(recent, "decisions: 0") {
		t.Errorf("since 1h: %v\n%s", err, recent)
	}
}

func TestMostCommonKeepsInsertionOrderForTies(t *testing.T) {
	var c Counter[string]
	for _, k := range []string{"b", "a", "c", "a", "c", "d"} {
		c.Add(k)
	}
	got := c.MostCommon(3)
	want := []Entry[string]{{"a", 2}, {"c", 2}, {"b", 1}}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("MostCommon = %v, want %v", got, want)
		}
	}
}
