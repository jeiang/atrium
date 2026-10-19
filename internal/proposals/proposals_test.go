package proposals_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
)

const clock = 1_792_080_000.0

func create(t *testing.T, d *db.DB, kind, title, fingerprint string) (int64, bool) {
	t.Helper()
	account := "icloud"
	id, ok, err := proposals.Create(context.Background(), d, kind, &account, title, py.Dict{}, py.Dict{}, fingerprint, clock)
	if err != nil {
		t.Fatal(err)
	}
	return id, ok
}

func status(t *testing.T, d *db.DB, id int64) string {
	t.Helper()
	p, err := proposals.Get(context.Background(), d.Read, id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Status
}

func proposalErr(t *testing.T, err error, want string) {
	t.Helper()
	var p proposals.ProposalError
	if !errors.As(err, &p) || err.Error() != want {
		t.Fatalf("err = %v, want ProposalError %q", err, want)
	}
}

func TestFingerprintDedupeFailedReopensAndDismissSuppress(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := context.Background()
	first, ok := create(t, d, "k", "t", "fp")
	if !ok {
		t.Fatal("first create refused")
	}
	if _, ok := create(t, d, "k", "t", "fp"); ok {
		t.Fatal("duplicate fingerprint created")
	}
	if err := proposals.Dismiss(ctx, d.Write, first, false, clock); err != nil {
		t.Fatal(err)
	}
	if _, ok := create(t, d, "k", "t", "fp"); ok {
		t.Fatal("dismissed fingerprint recreated")
	}
	other, _ := create(t, d, "k", "t", "fp2")
	if err := proposals.Dismiss(ctx, d.Write, other, true, clock); err != nil {
		t.Fatal(err)
	}
	if s := status(t, d, other); s != proposals.Suppressed {
		t.Fatalf("status = %s", s)
	}
	if _, ok := create(t, d, "k", "t", "fp2"); ok {
		t.Fatal("suppressed fingerprint recreated")
	}
	third, _ := create(t, d, "k", "t", "fp3")
	if _, err := d.Write.Exec("UPDATE proposals SET status = 'failed' WHERE id = ?", third); err != nil {
		t.Fatal(err)
	}
	if again, ok := create(t, d, "k", "t2", "fp3"); !ok || again != third {
		t.Fatalf("failed reopen = %d, %v", again, ok)
	}
	if s := status(t, d, third); s != proposals.Pending {
		t.Fatalf("status = %s", s)
	}
	byKind, err := proposals.CountsByKind(ctx, d.Read, proposals.Pending)
	if err != nil || py.JSONDumps(byKind) != `{"k": 1}` {
		t.Fatalf("counts by kind = %v, %v", byKind, err)
	}
	byStatus, err := proposals.CountsByStatus(ctx, d.Read)
	if err != nil || py.JSONDumps(byStatus) != `{"dismissed": 1, "pending": 1, "suppressed": 1, "all": 3}` {
		t.Fatalf("counts by status = %v, %v", byStatus, err)
	}
	proposalErr(t, proposals.Dismiss(ctx, d.Write, first, false, clock), "proposal 1 is not open")
}

func pipeline(d *db.DB) (*proposals.Pipeline, *proposals.Registry) {
	r := proposals.NewRegistry()
	return &proposals.Pipeline{DB: d, Registry: r, Now: func() float64 { return clock }}, r
}

func TestApproveErrors(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := context.Background()
	p, r := pipeline(d)
	_, err := p.ApproveAndApply(ctx, 9, nil)
	proposalErr(t, err, "unknown proposal 9")
	id, _ := create(t, d, "nothing", "t", "fp")
	_, err = p.ApproveAndApply(ctx, id, nil)
	proposalErr(t, err, "no executor for kind 'nothing'")
	r.Executors["nothing"] = func(context.Context, *proposals.Proposal, py.Dict) (py.Dict, error) {
		return py.Dict{{Key: "summary", Value: "done"}}, nil
	}
	done, err := p.ApproveAndApply(ctx, id, nil)
	if err != nil || done.Status != proposals.Applied || py.JSONDumps(done.Result) != `{"summary": "done"}` || *done.AppliedTS != clock {
		t.Fatalf("applied = %+v, %v", done, err)
	}
	_, err = p.ApproveAndApply(ctx, id, nil)
	proposalErr(t, err, "proposal 1 is not open")
	_, err = p.Undo(ctx, 9)
	proposalErr(t, err, "proposal 9 is not applied")
}

func TestFailureIsStoredWithItsPythonClass(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := context.Background()
	p, r := pipeline(d)
	var synced []string
	r.Sync = func(_ context.Context, account string) error {
		synced = append(synced, account)
		return nil
	}
	r.Executors["k"] = func(context.Context, *proposals.Proposal, py.Dict) (py.Dict, error) {
		return nil, mailops.Error("no Trash mailbox found")
	}
	id, _ := create(t, d, "k", "t", "fp")
	done, err := p.ApproveAndApply(ctx, id, nil)
	if err != nil || done.Status != proposals.Failed || py.JSONDumps(done.Result) != `{"error": "MailOpError: no Trash mailbox found"}` {
		t.Fatalf("failed = %+v, %v", done, err)
	}
	if !reflect.DeepEqual(synced, []string{"icloud"}) {
		t.Fatalf("synced = %v", synced)
	}
	r.Executors["k"] = func(context.Context, *proposals.Proposal, py.Dict) (py.Dict, error) {
		return nil, proposals.SelectionError("selection does not match the proposal groups")
	}
	_, err = p.ApproveAndApply(ctx, id, nil)
	var sel proposals.SelectionError
	if !errors.As(err, &sel) {
		t.Fatalf("selection error = %v", err)
	}
	if s := status(t, d, id); s != proposals.Pending {
		t.Fatalf("status after a selection error = %s", s)
	}
}

func TestSyncFailureIsReportedNotRaised(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := context.Background()
	p, r := pipeline(d)
	r.Sync = func(context.Context, string) error { return mailops.Error("server gone") }
	r.Executors["k"] = func(ctx context.Context, prop *proposals.Proposal, _ py.Dict) (py.Dict, error) {
		_, err := proposals.RecordAction(ctx, d.Write, prop.ID, py.Dict{{Key: "op", Value: "x"}}, py.Dict{{Key: "op", Value: "x"}}, true, nil, clock)
		return py.Dict{{Key: "summary", Value: "s"}}, err
	}
	r.Undoers["x"] = func(context.Context, json.RawMessage) (py.Dict, error) { return py.Dict{{Key: "n", Value: 1}}, nil }
	id, _ := create(t, d, "k", "t", "fp")
	done, err := p.ApproveAndApply(ctx, id, nil)
	if err != nil || py.JSONDumps(done.Result) != `{"summary": "s", "sync_error": "MailOpError: server gone"}` {
		t.Fatalf("applied = %+v, %v", done, err)
	}
	out, err := p.Undo(ctx, id)
	if err != nil || py.JSONDumps(out.Dict()) != `{"undone": true, "outcomes": [{"action": 1, "ok": true}], "sync_error": "MailOpError: server gone"}` {
		t.Fatalf("undo = %+v, %v", out, err)
	}
}

func TestUndoReplaysNewestFirstAndKeepsFailures(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := context.Background()
	p, r := pipeline(d)
	r.Executors["k"] = func(ctx context.Context, prop *proposals.Proposal, _ py.Dict) (py.Dict, error) {
		for _, op := range []string{"a", "b", "c"} {
			if _, err := proposals.RecordAction(ctx, d.Write, prop.ID, py.Dict{{Key: "op", Value: op}}, py.Dict{{Key: "op", Value: op}}, true, nil, clock); err != nil {
				return nil, err
			}
		}
		_, err := proposals.RecordAction(ctx, d.Write, prop.ID, py.Dict{{Key: "op", Value: "skip"}}, nil, false, nil, clock)
		return py.Dict{}, err
	}
	var order []string
	fail := true
	for _, op := range []string{"a", "b", "c"} {
		r.Undoers[op] = func(context.Context, json.RawMessage) (py.Dict, error) {
			order = append(order, op)
			if op == "b" && fail {
				return nil, mailops.Error("UIDVALIDITY changed")
			}
			return nil, nil
		}
	}
	id, _ := create(t, d, "k", "t", "fp")
	if _, err := p.ApproveAndApply(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	out, err := p.Undo(ctx, id)
	if err != nil || py.JSONDumps(out.Dict()) != `{"undone": false, "outcomes": [{"action": 3, "ok": true}, {"action": 2, "ok": false, "error": "UIDVALIDITY changed"}, {"action": 1, "ok": true}], "sync_error": null}` {
		t.Fatalf("undo = %s, %v", py.JSONDumps(out.Dict()), err)
	}
	if !reflect.DeepEqual(order, []string{"c", "b", "a"}) {
		t.Fatalf("order = %v", order)
	}
	logged, _ := proposals.Actions(ctx, d.Read, id)
	failed := logged[len(logged)-2]
	if failed.OK || *failed.Error != "MailOpError: UIDVALIDITY changed" || py.JSONDumps(failed.Action) != `{"op": "undo", "of": 2}` {
		t.Fatalf("failed undo action = %+v", failed)
	}
	if ok, _ := proposals.CanUndo(ctx, d.Read, id); !ok {
		t.Fatal("a partly undone proposal is not undoable")
	}
	fail = false
	order = nil
	out, err = p.Undo(ctx, id)
	if err != nil || !out.Undone || !reflect.DeepEqual(order, []string{"b"}) {
		t.Fatalf("second undo = %+v, %v, order %v", out, err, order)
	}
	got, _ := proposals.Get(ctx, d.Read, id)
	if got.Undoable || py.JSONDumps(got.Result) != `{"undone_ts": 1792080000.0}` {
		t.Fatalf("after undo = %+v", got)
	}
	if ok, _ := proposals.CanUndo(ctx, d.Read, id); ok {
		t.Fatal("undone proposal still undoable")
	}
}

func TestUndoWithoutHandlerStops(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := context.Background()
	p, r := pipeline(d)
	r.Executors["k"] = func(ctx context.Context, prop *proposals.Proposal, _ py.Dict) (py.Dict, error) {
		_, err := proposals.RecordAction(ctx, d.Write, prop.ID, py.Dict{{Key: "op", Value: "x"}}, py.Dict{{Key: "op", Value: "gone"}}, true, nil, clock)
		return py.Dict{}, err
	}
	id, _ := create(t, d, "k", "t", "fp")
	if _, err := p.ApproveAndApply(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	_, err := p.Undo(ctx, id)
	proposalErr(t, err, "no undo handler for 'gone'")
}

func TestListAndGetShapes(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := context.Background()
	account := "icloud"
	if _, _, err := proposals.Create(ctx, d, "a", &account, "first", py.Dict{{Key: "p", Value: 1}}, py.Dict{{Key: "v", Value: 2}}, "f1", 10); err != nil {
		t.Fatal(err)
	}
	if _, _, err := proposals.Create(ctx, d, "b", nil, "second", py.Dict{}, py.Dict{}, "f2", 20); err != nil {
		t.Fatal(err)
	}
	listed, err := proposals.List(ctx, d.Read, "", "")
	if err != nil || len(listed) != 2 || listed[0].Title != "second" {
		t.Fatalf("list = %v, %v", listed, err)
	}
	want := `{"id": 1, "kind": "a", "account": "icloud", "status": "pending", "created_ts": 10.0, "decided_ts": null, "applied_ts": null, ` +
		`"title": "first", "preview": {"v": 2}, "result": null, "fingerprint": "f1", "undoable": false}`
	if got := py.JSONDumps(listed[1].Dict()); got != want {
		t.Fatalf("listed = %s", got)
	}
	full, err := proposals.Get(ctx, d.Read, 1)
	if err != nil {
		t.Fatal(err)
	}
	want = `{"id": 1, "kind": "a", "account": "icloud", "status": "pending", "created_ts": 10.0, "decided_ts": null, "applied_ts": null, ` +
		`"title": "first", "preview": {"v": 2}, "result": null, "fingerprint": "f1", "payload": {"p": 1}, "undoable": false}`
	if got := py.JSONDumps(full.Dict()); got != want {
		t.Fatalf("get = %s", got)
	}
	if missing, err := proposals.Get(ctx, d.Read, 5); err != nil || missing != nil {
		t.Fatalf("missing = %v, %v", missing, err)
	}
	if kinds, _ := proposals.List(ctx, d.Read, proposals.Pending, "b"); len(kinds) != 1 || kinds[0].Account != nil {
		t.Fatalf("filtered = %v", kinds)
	}
	if n, _ := proposals.PendingCount(ctx, d.Read); n != 2 {
		t.Fatalf("pending = %d", n)
	}
}
