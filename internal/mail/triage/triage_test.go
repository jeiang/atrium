package triage

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/rulestest"
	"github.com/jeiang/atrium/internal/py"
)

type fields = rulestest.Fields

func decide(t *testing.T, d *db.DB, m int64, stage, dest string, ts float64) {
	t.Helper()
	rulestest.Exec(t, d, "INSERT INTO decisions (message_id, account, ts, stage, key, action, dest, confidence) VALUES (?, 'icloud', ?, ?, 'k', 'file', ?, 0.9)",
		m, ts, stage, dest)
}

func filed(t *testing.T, d *db.DB, uid int, folder, addr, digest string) int64 {
	t.Helper()
	return rulestest.Filed(t, d, uid, folder, addr, "example.test", nil, fields{"digest": digest})
}

func rowsBy(p Page) map[int64]Row {
	out := map[int64]Row{}
	for _, r := range p.Rows {
		out[r.MessageID] = r
	}
	return out
}

func TestTriageRowsAgreementAndFilters(t *testing.T) {
	d := rulestest.DB(t)
	ctx := t.Context()
	agree := filed(t, d, 1, "Bills", "a@x.test", "same1")
	decide(t, d, agree, "operator", "Bills", 1)
	disagree := filed(t, d, 2, "Other", "b@x.test", "same2")
	decide(t, d, disagree, "knn", "Bills", 1)
	pending := rulestest.Inbox(t, d, 3, "new@sender.test", "sender.test", nil, fields{"digest": "same3"})
	decide(t, d, pending, "learned", "Bills", 1)
	gone := filed(t, d, 4, "Bills", "c@x.test", "same4")
	rulestest.Exec(t, d, "UPDATE messages SET gone_at = 1 WHERE id = ?", gone)
	decide(t, d, gone, "llm", "Bills", 1)
	decide(t, d, agree, "knn", "Bills", 1)

	page, err := Triage(ctx, d.Read, Filter{Account: "icloud"})
	if err != nil || page.Total != 4 {
		t.Fatalf("Triage = %+v, %v", page, err)
	}
	by := rowsBy(page)
	if r := by[agree]; r.Stage != "knn" || r.Agreement != "agree" {
		t.Errorf("agree row = %+v", r)
	}
	if r := by[disagree]; r.Agreement != "disagree" || r.Current == nil || *r.Current != "Other" {
		t.Errorf("disagree row = %+v", r)
	}
	if r := by[pending]; r.Agreement != "pending" || r.Current == nil || *r.Current != "INBOX" {
		t.Errorf("pending row = %+v", r)
	}
	if r := by[gone]; r.Agreement != "gone" || r.Current != nil {
		t.Errorf("gone row = %+v", r)
	}
	if p, err := Triage(ctx, d.Read, Filter{Account: "icloud", Agreement: "disagree"}); err != nil || p.Total != 1 {
		t.Errorf("disagree filter = %+v, %v", p, err)
	}
	if p, err := Triage(ctx, d.Read, Filter{Account: "icloud", Stage: "llm"}); err != nil || len(p.Rows) != 1 || p.Rows[0].MessageID != gone {
		t.Errorf("stage filter = %+v, %v", p, err)
	}
	if p, err := Triage(ctx, d.Read, Filter{Account: "gmail"}); err != nil || p.Total != 0 || p.Rows == nil {
		t.Errorf("gmail = %+v, %v", p, err)
	}
	if p, err := Triage(ctx, d.Read, Filter{Account: "icloud", Limit: 2, Offset: 3}); err != nil || len(p.Rows) != 1 {
		t.Errorf("paging = %+v, %v", p, err)
	}
	if _, err := Triage(ctx, d.Read, Filter{Stage: "bogus"}); err == nil || err.Error() != "unknown stage 'bogus'" {
		t.Errorf("bogus stage: %v", err)
	}
	if _, err := Triage(ctx, d.Read, Filter{Agreement: "bogus"}); err == nil || err.Error() != "unknown agreement 'bogus'" {
		t.Errorf("bogus agreement: %v", err)
	}
}

func TestTriageTextFilterAndRow(t *testing.T) {
	d := rulestest.DB(t)
	a := filed(t, d, 1, "Bills", "ann@x.test", "d1")
	decide(t, d, a, "operator", "Bills", 1)
	b := filed(t, d, 2, "Bills", "bob@x.test", "d2")
	decide(t, d, b, "operator", "Bills", 1)
	rulestest.Exec(t, d, "INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', 'Elsewhere', 1, 'operator')", b)
	for q, want := range map[string]int{"ANN@": 1, "elsewhere": 1, "bills": 2, "icloud": 2, "nothing": 0} {
		if p, err := Triage(t.Context(), d.Read, Filter{Q: q}); err != nil || p.Total != want {
			t.Errorf("q=%q: %+v, %v; want %d", q, p, err, want)
		}
	}
	row, err := Get(t.Context(), d.Read, b)
	if err != nil || row == nil || row.CorrectedTo == nil || *row.CorrectedTo != "Elsewhere" {
		t.Errorf("Get = %+v, %v", row, err)
	}
	if row, err := Get(t.Context(), d.Read, 999); err != nil || row != nil {
		t.Errorf("Get(999) = %+v, %v", row, err)
	}
}

func TestTriageAppliedAction(t *testing.T) {
	d := rulestest.DB(t)
	m := filed(t, d, 1, "Bills", "a@x.test", "d1")
	decide(t, d, m, "learned", "Bills", 1)
	rulestest.Exec(t, d, "INSERT INTO auto_actions (ts, account, message_id, digest, kind, dest, undo, ok) VALUES (1, 'icloud', ?, 'd1', 'file', 'Bills', '{}', 1)", m)
	row, err := Get(t.Context(), d.Read, m)
	if err != nil || row == nil || row.Applied == nil {
		t.Fatalf("Get = %+v, %v", row, err)
	}
	if a := *row.Applied; a.Kind != "file" || a.Status != "applied" || !a.Undoable || a.Error != nil {
		t.Errorf("applied = %+v", a)
	}
	rulestest.Exec(t, d, "UPDATE auto_actions SET undone_ts = 2 WHERE message_id = ?", m)
	row, _ = Get(t.Context(), d.Read, m)
	if a := *row.Applied; a.Status != "undone" || a.Undoable {
		t.Errorf("after undo = %+v", a)
	}
	rulestest.Exec(t, d, "UPDATE auto_actions SET undone_ts = NULL, ok = NULL WHERE message_id = ?", m)
	row, _ = Get(t.Context(), d.Read, m)
	if a := *row.Applied; a.Status != "interrupted" || a.Undoable {
		t.Errorf("held = %+v", a)
	}
}

func TestTriageSummaryAndFlags(t *testing.T) {
	d := rulestest.DB(t)
	ctx := t.Context()
	a := filed(t, d, 1, "Bills", "a@x.test", "d1")
	decide(t, d, a, "operator", "Bills", 1)
	b := filed(t, d, 2, "Other", "b@x.test", "d2")
	decide(t, d, b, "knn", "Bills", 1)
	s, err := Summarize(ctx, d.Read, config.Map(nil), "icloud", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if s.Decisions != 2 || s.Moved != 2 || s.Agreed != 1 || s.AgreementPct == nil || *s.AgreementPct != 50.0 ||
		s.ByStage["operator"] != 1 || s.ByStage["correction"] != 0 || len(s.ByStage) != 6 {
		t.Errorf("summary = %+v", s)
	}
	u := rulestest.Inbox(t, d, 5, "new@sender.test", "sender.test", nil, fields{"seen": 0})
	rulestest.Exec(t, d, "INSERT INTO flag_decisions (message_id, account, ts, contacts_match, llm_flag) VALUES (?, 'icloud', 0, 1, 1)", u)
	s, err = Summarize(ctx, d.Read, config.Map(nil), "icloud", nil, time.Now())
	if err != nil || s.Flags.Both != 1 {
		t.Errorf("flags = %+v, %v", s.Flags, err)
	}
	js, err := json.Marshal(s.TopDestinations)
	if err != nil || string(js) != `[["Bills",2]]` {
		t.Errorf("top destinations = %s, %v", js, err)
	}
	all, err := Summaries(ctx, d.Read, config.Map(nil), nil, time.Now())
	if err != nil || len(all) != 2 || all[0].Account != "icloud" || all[1].Account != "gmail" {
		t.Errorf("Summaries = %+v, %v", all, err)
	}
	judged, err := FlagJudgments(ctx, d.Read, "icloud", 0, 0)
	if err != nil || len(judged) != 1 || judged[0].MessageID != u {
		t.Errorf("FlagJudgments = %+v, %v", judged, err)
	}
	if none, err := FlagJudgments(ctx, d.Read, "gmail", 0, 0); err != nil || none == nil || len(none) != 0 {
		t.Errorf("gmail judgments = %+v, %v", none, err)
	}
}

func TestUnknownFilterIsAFilterError(t *testing.T) {
	d := rulestest.DB(t)
	for _, f := range []Filter{{Stage: "nonsense"}, {Agreement: "nonsense"}} {
		_, err := Triage(t.Context(), d.Read, f)
		var filter FilterError
		if !errors.As(err, &filter) || py.ExceptionClass(err) != "ValueError" {
			t.Errorf("Triage(%+v) error = %v", f, err)
		}
	}
	if _, err := Triage(t.Context(), d.Read, Filter{Stage: "llm", Agreement: "gone"}); err != nil {
		t.Error(err)
	}
}

func TestFolders(t *testing.T) {
	d := rulestest.DB(t)
	filed(t, d, 1, "Fresh", "a@x.test", "d1")
	got, err := Folders(t.Context(), d.Read, "icloud")
	if err != nil || fmt.Sprint(got) != "[{Fresh 1}]" {
		t.Errorf("Folders = %v, %v", got, err)
	}
	if got, err := Folders(t.Context(), d.Read, "gmail"); err != nil || got == nil || strings.TrimSpace(fmt.Sprint(got)) != "[]" {
		t.Errorf("empty Folders = %v, %v", got, err)
	}
}
