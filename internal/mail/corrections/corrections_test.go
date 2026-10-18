package corrections_test

import (
	"errors"
	"testing"

	"github.com/jeiang/atrium/internal/mail/corrections"
	"github.com/jeiang/atrium/internal/mail/router"
	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/mail/rulestest"
)

func TestCorrectionCountsAsFiledForLearnedRules(t *testing.T) {
	d := rulestest.DB(t)
	ctx := t.Context()
	for i := range 2 {
		rulestest.Filed(t, d, i+1, "Bills", "pay@bank.test", "bank.test", nil, nil)
	}
	target := rulestest.Inbox(t, d, 10, "pay@bank.test", "bank.test", nil, nil)
	m := rulestest.Fetch(t, d, target)
	if l, err := rules.LearnedMatch(ctx, d.Read, &m); err != nil || l != nil {
		t.Fatalf("learned before the correction: %+v, %v", l, err)
	}
	c := rulestest.Inbox(t, d, 11, "pay@bank.test", "bank.test", nil, nil)
	if _, err := corrections.Record(ctx, d, c, "Bills", corrections.Source, 5); err != nil {
		t.Fatal(err)
	}
	var folder string
	if err := d.Read.QueryRowContext(ctx, "SELECT folder FROM filed_messages WHERE id = ?", c).Scan(&folder); err != nil || folder != "Bills" {
		t.Fatalf("filed folder %q, %v", folder, err)
	}
	l, err := rules.LearnedMatch(ctx, d.Read, &m)
	if err != nil || l == nil || l.Dest != "Bills" || l.N != 3 {
		t.Errorf("learned after the correction: %+v, %v", l, err)
	}
}

func TestCorrectionFeedsNeighbours(t *testing.T) {
	d := rulestest.DB(t)
	ctx := t.Context()
	c := rulestest.Inbox(t, d, 1, "x@y.test", "y.test", rulestest.Vector(1), nil)
	if _, err := corrections.Record(ctx, d, c, "Fresh", corrections.Source, 1); err != nil {
		t.Fatal(err)
	}
	probe := rulestest.Fetch(t, d, rulestest.Inbox(t, d, 2, "q@z.test", "z.test", rulestest.Vector(1), nil))
	neighbors, err := router.NearestFiled(ctx, d.Read, &probe, 3)
	if err != nil || len(neighbors) != 1 || neighbors[0].Folder != "Fresh" {
		t.Errorf("neighbors %+v, %v", neighbors, err)
	}
}

func TestRecordLogsDecisionAndValidates(t *testing.T) {
	d := rulestest.DB(t)
	ctx := t.Context()
	m := rulestest.Inbox(t, d, 1, "new@sender.test", "sender.test", nil, nil)
	got, err := corrections.Record(ctx, d, m, " Bills ", corrections.Source, 7)
	if err != nil || got != (corrections.Correction{MessageID: m, Account: "icloud", Folder: "Bills", TS: 7, Source: "operator"}) {
		t.Fatalf("Record = %+v, %v", got, err)
	}
	if _, err := corrections.Record(ctx, d, m, "Taxes", corrections.Source, 8); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Read.QueryContext(ctx, "SELECT stage, dest, key, action, confidence, detail FROM decisions WHERE message_id = ? ORDER BY id", m)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for rows.Next() {
		var stage, dest, key, action, detail string
		var conf float64
		if err := rows.Scan(&stage, &dest, &key, &action, &conf, &detail); err != nil {
			t.Fatal(err)
		}
		seen = append(seen, stage+"/"+dest+"/"+key+"/"+action+"/"+detail)
		if conf != 1.0 {
			t.Errorf("confidence %v", conf)
		}
	}
	_ = rows.Close()
	if len(seen) != 2 || seen[0] != "correction/Bills/operator/file/{}" || seen[1] != "correction/Taxes/operator/file/{}" {
		t.Errorf("decisions %v", seen)
	}
	if n := rulestest.Count(t, d, "SELECT COUNT(*) FROM corrections"); n != 1 {
		t.Errorf("%d corrections", n)
	}
	pending, err := router.Eligible(ctx, d.Read, "icloud")
	if err != nil || len(pending) != 0 {
		t.Errorf("eligible after a decision: %v, %v", pending, err)
	}
	for _, bad := range []string{"", "a|b", "INBOX", "   ", string(make([]rune, 201))} {
		var ce *corrections.Error
		if _, err := corrections.Record(ctx, d, m, bad, corrections.Source, 9); !errors.As(err, &ce) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	var ce *corrections.Error
	if _, err := corrections.Record(ctx, d, 999, "X", corrections.Source, 9); !errors.As(err, &ce) || ce.Msg != "unknown message 999" {
		t.Errorf("unknown message: %v", err)
	}
	sent := rulestest.Add(t, d, 5, nil, "", rulestest.Fields{"sent": 1})
	if _, err := corrections.Record(ctx, d, sent, "X", corrections.Source, 9); !errors.As(err, &ce) || ce.Msg != "cannot correct a sent message" {
		t.Errorf("sent message: %v", err)
	}
	if _, err := corrections.Record(ctx, d, m, "|", corrections.Source, 9); err == nil || err.Error() != "invalid destination '|'" {
		t.Errorf("repr in error: %v", err)
	}
}

func TestClearAndList(t *testing.T) {
	d := rulestest.DB(t)
	ctx := t.Context()
	a := rulestest.Inbox(t, d, 1, "a@a.test", "a.test", nil, nil)
	b := rulestest.Add(t, d, 2, nil, "", rulestest.Fields{"account": "gmail", "mailbox_id": 2})
	for _, c := range []struct {
		id int64
		ts float64
	}{{a, 1}, {b, 2}} {
		if _, err := corrections.Record(ctx, d, c.id, "Bills", corrections.Source, c.ts); err != nil {
			t.Fatal(err)
		}
	}
	all, err := corrections.List(ctx, d.Read, "", corrections.DefaultListLimit)
	if err != nil || len(all) != 2 || all[0].MessageID != b || all[1].MessageID != a {
		t.Errorf("List = %+v, %v", all, err)
	}
	icloud, err := corrections.List(ctx, d.Read, "icloud", 1)
	if err != nil || len(icloud) != 1 || icloud[0].MessageID != a {
		t.Errorf("List icloud = %+v, %v", icloud, err)
	}
	wrong := "Taxes"
	if ok, err := corrections.Clear(ctx, d, a, &wrong); err != nil || ok {
		t.Errorf("Clear wrong folder = %v, %v", ok, err)
	}
	right := "Bills"
	if ok, err := corrections.Clear(ctx, d, a, &right); err != nil || !ok {
		t.Errorf("Clear = %v, %v", ok, err)
	}
	if ok, err := corrections.Clear(ctx, d, b, nil); err != nil || !ok {
		t.Errorf("Clear any folder = %v, %v", ok, err)
	}
	if got, err := corrections.Get(ctx, d.Read, a); err != nil || got != nil {
		t.Errorf("Get after clear = %+v, %v", got, err)
	}
	if empty, err := corrections.List(ctx, d.Read, "", 10); err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("empty List = %#v, %v", empty, err)
	}
}
