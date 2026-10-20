package openloops_test

import (
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail/openloops"
)

func TestOweReplyWhenHumanWroteLast(t *testing.T) {
	d := newDB(t)
	mail(t, d, 1, 2, nil)
	if got := compute(t, d, NOW()); got != (openloops.Counts{OweReply: 1}) {
		t.Fatalf("counts = %+v", got)
	}
	for _, s := range states(t, d) {
		if s != (state{"owe_reply", "pat@person.test"}) {
			t.Errorf("state = %+v", s)
		}
	}
}

func TestReplyClearsOweAndStartsWaiting(t *testing.T) {
	d := newDB(t)
	mail(t, d, 1, 6, fields{"in_reply_to": "", "refs": ""})
	sent(t, d, 2, 5, "pat@person.test", fields{"in_reply_to": "m1@x.test", "subject": "Re: Hello"})
	compute(t, d, NOW())
	if got := states(t, d); len(got) != 1 {
		t.Fatalf("states = %v", got)
	}
	var since float64
	var subject, st, counterpart string
	if err := d.Read.QueryRow("SELECT since, subject, state, counterpart FROM open_loops").Scan(&since, &subject, &st, &counterpart); err != nil {
		t.Fatal(err)
	}
	if st != "waiting_on_them" || counterpart != "pat@person.test" || math.Abs(since-ago(5)) > 1e-6 || subject != "Hello" {
		t.Errorf("row = %v %v %v %v", since, subject, st, counterpart)
	}
}

func TestRecentSentIsNotWaitingYet(t *testing.T) {
	d := newDB(t)
	sent(t, d, 1, 1, "pat@person.test", nil)
	if got := compute(t, d, NOW()); got != (openloops.Counts{}) {
		t.Errorf("counts = %+v", got)
	}
}

func TestTheirReplyAfterOursMakesItOweAgain(t *testing.T) {
	d := newDB(t)
	sent(t, d, 1, 8, "pat@person.test", nil)
	mail(t, d, 2, 2, fields{"in_reply_to": "m1@x.test", "refs": "m1@x.test", "subject": "Re: Hello"})
	compute(t, d, NOW())
	if got := stateList(t, d); !slices.Equal(got, []string{"owe_reply"}) {
		t.Errorf("states = %v", got)
	}
}

func TestSubjectFallbackThreadsHeaderlessMail(t *testing.T) {
	d := newDB(t)
	mail(t, d, 1, 6, fields{"subject": "Lunch plans"})
	sent(t, d, 2, 5, "pat@person.test", fields{"subject": "Re: Lunch plans"})
	compute(t, d, NOW())
	if got := stateList(t, d); !slices.Equal(got, []string{"waiting_on_them"}) {
		t.Errorf("states = %v", got)
	}
}

func TestAutomatedAndListMailDoesNotOwe(t *testing.T) {
	d := newDB(t)
	mail(t, d, 1, 2, fields{"from_addr": "noreply@shop.test", "subject": "Receipt"})
	mail(t, d, 2, 2, fields{"list_id": "<news.list>", "subject": "Digest"})
	mail(t, d, 3, 2, fields{"precedence": "bulk", "subject": "Promo"})
	compute(t, d, NOW())
	if got := states(t, d); len(got) != 0 {
		t.Errorf("states = %v", got)
	}
}

func TestFlagDecisionPersonAskingOverridesHeuristic(t *testing.T) {
	d := newDB(t)
	quiet := mail(t, d, 1, 2, fields{"subject": "FYI"})
	asks := mail(t, d, 2, 2, fields{"from_addr": "noreply@svc.test", "subject": "Question"})
	for id, asking := range map[int64]int{quiet: 0, asks: 1} {
		exec(t, d, "INSERT INTO flag_decisions (message_id, account, ts, contacts_match, person_asking) VALUES (?, 'icloud', 0, 0, ?)", id, asking)
	}
	compute(t, d, NOW())
	got := states(t, d)
	if len(got) != 1 {
		t.Fatalf("states = %v", got)
	}
	for _, s := range got {
		if s != (state{"owe_reply", "noreply@svc.test"}) {
			t.Errorf("state = %+v", s)
		}
	}
}

func TestGmailDraftsAreNotOutbound(t *testing.T) {
	d := newDB(t)
	mail(t, d, 1, 6, nil)
	mail(t, d, 2, 5, fields{"sent": 1, "from_addr": "me@mine.test", "to_addrs": []string{"pat@person.test"}, "labels": `["\\Draft"]`, "in_reply_to": "m1@x.test"})
	compute(t, d, NOW())
	if got := stateList(t, d); !slices.Equal(got, []string{"owe_reply"}) {
		t.Errorf("states = %v", got)
	}
}

func visible(t *testing.T, d *db.DB, at float64) []openloops.Loop {
	t.Helper()
	loops, err := openloops.ListLoops(ctx, d.Read, at, "", openloops.DefaultListLimit, false)
	if err != nil {
		t.Fatal(err)
	}
	return loops
}

func TestSnoozeAndDismissSurviveRecomputeUntilThreadChanges(t *testing.T) {
	d := newDB(t)
	mail(t, d, 1, 2, nil)
	compute(t, d, NOW())
	loop := visible(t, d, NOW())[0]
	if ok, err := openloops.Snooze(ctx, d.Write, loop.ID, NOW()+openloops.Day); err != nil || !ok {
		t.Fatalf("Snooze = %v, %v", ok, err)
	}
	compute(t, d, NOW())
	if got := visible(t, d, NOW()); len(got) != 0 {
		t.Errorf("snoozed loop listed: %+v", got)
	}
	if got := visible(t, d, NOW()+2*openloops.Day); len(got) != 1 {
		t.Errorf("woken loop not listed: %+v", got)
	}
	if ok, err := openloops.Dismiss(ctx, d.Write, loop.ID); err != nil || !ok {
		t.Fatalf("Dismiss = %v, %v", ok, err)
	}
	compute(t, d, NOW())
	if got := visible(t, d, NOW()+2*openloops.Day); len(got) != 0 {
		t.Errorf("dismissed loop listed: %+v", got)
	}
	mail(t, d, 2, 1, fields{"in_reply_to": "m1@x.test", "subject": "Re: Hello"})
	compute(t, d, NOW())
	if got := visible(t, d, NOW()); len(got) != 1 {
		t.Errorf("new activity did not reopen the loop: %+v", got)
	}
	if ok, err := openloops.Dismiss(ctx, d.Write, 999); err != nil || ok {
		t.Errorf("Dismiss(999) = %v, %v", ok, err)
	}
}

func TestDismissAllClearsOneStateAndSurvivesRecompute(t *testing.T) {
	d := newDB(t)
	mail(t, d, 1, 2, fields{"subject": "First"})
	mail(t, d, 2, 2, fields{"subject": "Second"})
	sent(t, d, 3, 5, "pat@person.test", fields{"subject": "Quote?"})
	compute(t, d, NOW())
	if n, err := openloops.DismissAll(ctx, d.Write, "owe_reply"); err != nil || n != 2 {
		t.Fatalf("DismissAll = %d, %v", n, err)
	}
	compute(t, d, NOW())
	got := visible(t, d, NOW())
	if len(got) != 1 || got[0].State != "waiting_on_them" {
		t.Errorf("visible = %+v", got)
	}
	if _, err := openloops.DismissAll(ctx, d.Write, "everything"); !errors.Is(err, httpx.ErrValue) {
		t.Errorf("DismissAll(everything) = %v", err)
	}
}

func TestResolvedLoopsAreRemoved(t *testing.T) {
	d := newDB(t)
	mail(t, d, 1, 2, nil)
	compute(t, d, NOW())
	sent(t, d, 2, 1, "pat@person.test", fields{"in_reply_to": "m1@x.test"})
	compute(t, d, NOW())
	if got := states(t, d); len(got) != 0 {
		t.Errorf("states = %v", got)
	}
}

func TestListLoopsOrderAndDays(t *testing.T) {
	d := newDB(t)
	mail(t, d, 1, 2.5, fields{"subject": "Newer"})
	mail(t, d, 2, 9, fields{"subject": "Older"})
	compute(t, d, NOW())
	loops, err := openloops.ListLoops(ctx, d.Read, NOW(), "owe_reply", 1, false)
	if err != nil || len(loops) != 1 || loops[0].Subject != "Older" || loops[0].Days != 9 {
		t.Fatalf("ListLoops = %+v, %v", loops, err)
	}
	if got, _ := openloops.ListLoops(ctx, d.Read, NOW(), "waiting_on_them", 50, true); len(got) != 0 {
		t.Errorf("waiting = %+v", got)
	}
}

func TestNormSubject(t *testing.T) {
	for in, want := range map[string]string{
		"Re: Fwd: AW:  Lunch   plans ": "lunch plans", "SV : RE:hello": "hello", "Resume": "resume", "": "", "Fw: x": "x",
	} {
		if got, err := openloops.NormSubject(in); err != nil || got != want {
			t.Errorf("NormSubject(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
