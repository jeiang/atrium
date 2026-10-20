package live_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail/corrections"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/live/livetest"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/safety"
)

const (
	seen    = livetest.Seen
	flagged = livetest.Flagged
)

func uids(box *mailtest.FakeBox) []uint32 { return box.UIDs() }

func verbs(f *mailtest.FakeClient) []string {
	var out []string
	for _, w := range f.Writes {
		out = append(out, w.Verb)
	}
	return out
}

func baseline(t *testing.T, w *livetest.World) []int64 {
	t.Helper()
	rows, err := w.DB.Read.Query("SELECT message_id FROM live_baseline ORDER BY message_id")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func undoErr(t *testing.T, err error, want string) {
	t.Helper()
	var u live.UndoError
	if !errors.As(err, &u) || u.Error() != want {
		t.Fatalf("undo error = %v, want UndoError(%q)", err, want)
	}
}

func TestBaselineHoldsOnlyMailEligibleAtGoLive(t *testing.T) {
	w := livetest.ICloud(t, "live",
		livetest.Seed{Subject: "old", Flags: []string{seen}},
		livetest.Seed{Subject: "unread"},
		livetest.Seed{Subject: "starred", Flags: []string{seen, flagged}})
	w.Arrive("new")
	if got, want := baseline(t, w), []int64{w.MID("old")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("baseline = %v, want %v", got, want)
	}
	golive, err := live.GetState(t.Context(), w.DB.Read, live.GoliveKey)
	if err != nil || golive == nil || *golive != "1792080000.0" {
		t.Fatalf("golive_ts = %v, %v", golive, err)
	}
	called := false
	took, err := w.Live.EnsureGolive(t.Context(), func() error { called = true; return nil })
	if err != nil || took || called {
		t.Fatalf("EnsureGolive after go-live = %v, %v, before called %v", took, err, called)
	}
	w.Clock = 1_792_090_000
	n, err := w.Live.Rebaseline(t.Context())
	if err != nil || n != 2 {
		t.Fatalf("Rebaseline = %d, %v", n, err)
	}
	if got := baseline(t, w); !reflect.DeepEqual(got, []int64{w.MID("old"), w.MID("new")}) {
		t.Fatalf("rebaseline = %v", got)
	}
	if golive, _ := live.GetState(t.Context(), w.DB.Read, live.GoliveKey); *golive != "1792090000.0" {
		t.Fatalf("golive_ts after rebaseline = %s", *golive)
	}
}

func TestEnsureGoliveRunsBeforeOnlyTheFirstTime(t *testing.T) {
	w := livetest.ICloud(t, "live")
	w.Exec("DELETE FROM live_state")
	var order []string
	took, err := w.Live.EnsureGolive(t.Context(), func() error { order = append(order, "before"); return nil })
	if err != nil || !took || !reflect.DeepEqual(order, []string{"before"}) {
		t.Fatalf("EnsureGolive = %v, %v, %v", took, err, order)
	}
	w.Exec("DELETE FROM live_state")
	boom := errors.New("sync failed")
	if _, err := w.Live.EnsureGolive(t.Context(), func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("EnsureGolive with failing before = %v", err)
	}
	if golive, _ := live.GetState(t.Context(), w.DB.Read, live.GoliveKey); golive != nil {
		t.Fatal("a failed before must not record go-live")
	}
}

func TestQueryParamsGoliveIsInfinityWhenUnset(t *testing.T) {
	w := livetest.ICloud(t, "live")
	p, err := live.QueryParams(t.Context(), w.DB.Read, config.ICloud)
	if err != nil || p.Golive != 1_792_080_000 || p.Attempts != config.WriteAttempts || p.Account != config.ICloud {
		t.Fatalf("QueryParams = %+v, %v", p, err)
	}
	w.Exec("DELETE FROM live_state")
	p, err = live.QueryParams(t.Context(), w.DB.Read, config.ICloud)
	if err != nil || !math.IsInf(p.Golive, 1) {
		t.Fatalf("QueryParams before go-live = %+v, %v", p, err)
	}
	var newer bool
	if err := w.DB.Read.QueryRow("SELECT 1e308 >= :golive", p.Args()...).Scan(&newer); err != nil || newer {
		t.Fatalf("a correction at 1e308 counts as after an unset go-live: %v %v", newer, err)
	}
}

func TestInterruptedActionIsFlaggedAndNeverRetried(t *testing.T) {
	w := livetest.ICloud(t, "live")
	w.Arrive("crashy")
	mid := w.Decide("crashy", "file", new("Bills"), "learned")
	v, err := mailops.Snapshots(t.Context(), w.DB.Read, []int64{mid})
	if err != nil {
		t.Fatal(err)
	}
	src := new(config.Inbox)
	id, ok, err := w.Live.Begin(t.Context(), config.ICloud, mid, v[0].Digest, live.File, src, new("Bills"), py.Dict{}, nil)
	if err != nil || !ok {
		t.Fatalf("Begin = %d %v %v", id, ok, err)
	}
	if _, ok, err := w.Live.Begin(t.Context(), config.ICloud, mid, v[0].Digest, live.File, src, new("Bills"), py.Dict{}, nil); err != nil || ok {
		t.Fatalf("a held write must block its twin: %v %v", ok, err)
	}
	n, err := w.Live.Recover(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("Recover = %d, %v", n, err)
	}
	if n, _ := w.Live.Recover(t.Context()); n != 0 {
		t.Fatalf("second Recover = %d", n)
	}
	a, _ := live.Get(t.Context(), w.DB.Read, id)
	if a.OK != nil || a.Error == nil || *a.Error != live.Interrupted || a.Status != live.Held || a.Undoable {
		t.Fatalf("recovered action = %+v", a)
	}
	p, _ := live.QueryParams(t.Context(), w.DB.Read, config.ICloud)
	var eligible int
	if err := w.DB.Read.QueryRow("SELECT COUNT(*) FROM messages m WHERE m.id = :mid AND "+live.NotBlocked,
		append(p.Args(), sql.Named("mid", mid))...).Scan(&eligible); err != nil || eligible != 0 {
		t.Fatalf("interrupted action left the message eligible for the next pass: %d %v", eligible, err)
	}
	if _, ok, _ := w.Live.Begin(t.Context(), config.ICloud, mid, v[0].Digest, live.File, src, new("Bills"), py.Dict{}, nil); ok {
		t.Fatal("an interrupted write was begun again")
	}
	if w.Live.Discard(t.Context(), id) != nil || len(w.Actions()) != 1 {
		t.Fatal("discard removed a flagged interrupted row")
	}
	st, _ := w.Live.Status(t.Context())
	if got, _ := st.Get("actions"); !reflect.DeepEqual(got, py.Dict{{Key: "file", Value: py.Dict{{Key: "interrupted", Value: int64(1)}}}}) {
		t.Fatalf("status actions = %v", got)
	}
	if len(w.Fake.Writes) != 0 {
		t.Fatalf("writes = %v", w.Fake.Writes)
	}
}

func TestICloudFileUndoMovesBackAndIsNotUndoneTwice(t *testing.T) {
	w := livetest.ICloud(t, "live")
	w.Arrive("oops")
	id := w.File("oops", live.File, "Bills")
	if got := verbs(w.Fake); !reflect.DeepEqual(got, []string{"COPY", "STORE", "EXPUNGE"}) {
		t.Fatalf("writes = %v", got)
	}
	a, _ := live.Get(t.Context(), w.DB.Read, id)
	if a.Status != live.Applied || !a.Undoable || *a.After != `{"imap_name": "Bills", "uidvalidity": 3, "uid": 1}` {
		t.Fatalf("filed action = %+v", a)
	}
	w.Sync()
	out, err := w.Live.Undo(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	want := live.UndoResult{ID: id, Undone: true, Detail: py.Dict{{Key: "restored", Value: 1}, {Key: "missing", Value: 0}}}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("Undo = %+v", out)
	}
	if len(uids(w.Fake.Boxes["Bills"])) != 0 || len(uids(w.Fake.Boxes["INBOX"])) != 1 {
		t.Fatal("message not moved back")
	}
	if !reflect.DeepEqual(w.Synced, []string{config.ICloud}) {
		t.Fatalf("synced = %v", w.Synced)
	}
	a, _ = live.Get(t.Context(), w.DB.Read, id)
	if a.Status != live.Undone || a.UndoneTS == nil || a.Undoable {
		t.Fatalf("undone action = %+v", a)
	}
	_, err = w.Live.Undo(t.Context(), id)
	undoErr(t, err, "action "+strconv.FormatInt(id, 10)+" cannot be undone")
	if _, ok, _ := w.Live.Begin(t.Context(), config.ICloud, a.MessageID, a.Digest, live.File, a.Src, a.Dest, py.Dict{}, nil); !ok {
		t.Fatal("an undone action keeps blocking a new attempt of the same message id")
	}
}

func TestUndoErrors(t *testing.T) {
	w := livetest.ICloud(t, "live")
	w.Arrive("m")
	mid := w.MID("m")
	_, err := w.Live.Undo(t.Context(), 999)
	undoErr(t, err, "unknown action 999")

	held, _, _ := w.Live.Begin(t.Context(), config.ICloud, mid, "d1", live.Flag, nil, nil, py.Dict{}, nil)
	_, err = w.Live.Undo(t.Context(), held)
	undoErr(t, err, "action "+strconv.FormatInt(held, 10)+" cannot be undone")

	noPlan, _, _ := w.Live.Begin(t.Context(), config.ICloud, mid, "d2", live.Trash, nil, nil, py.Dict{}, nil)
	if err := w.Live.Finish(t.Context(), noPlan, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	_, err = w.Live.Undo(t.Context(), noPlan)
	undoErr(t, err, "action "+strconv.FormatInt(noPlan, 10)+" cannot be undone")

	bogus, _, _ := w.Live.Begin(t.Context(), config.ICloud, mid, "d3", live.File, nil, new("X"), py.Dict{}, nil)
	if err := w.Live.Finish(t.Context(), bogus, nil, py.Dict{{Key: "op", Value: "bogus"}}, nil); err != nil {
		t.Fatal(err)
	}
	_, err = w.Live.Undo(t.Context(), bogus)
	undoErr(t, err, "no undo handler for 'bogus'")

	stale, _, _ := w.Live.Begin(t.Context(), config.ICloud, mid, "d4", live.File, nil, new("Bills"), py.Dict{}, nil)
	plan := py.Dict{
		{Key: "op", Value: live.OpMoveBack}, {Key: "account", Value: config.ICloud}, {Key: "src_imap", Value: "INBOX"},
		{Key: "dest_imap", Value: "Bills"}, {Key: "dest_uidvalidity", Value: 99}, {Key: "items", Value: []any{}},
	}
	if err := w.Live.Partial(t.Context(), stale, "partial: x", nil, plan); err != nil {
		t.Fatal(err)
	}
	_, err = w.Live.Undo(t.Context(), stale)
	undoErr(t, err, "MailOpError: destination UIDVALIDITY changed")
	a, _ := live.Get(t.Context(), w.DB.Read, stale)
	if a.UndoneError == nil || *a.UndoneError != "MailOpError: destination UIDVALIDITY changed" || !a.Undoable || *a.After != "null" {
		t.Fatalf("failed undo = %+v", a)
	}
	if len(w.Synced) != 0 || len(w.Fake.Writes) != 0 {
		t.Fatalf("a failed undo synced %v or wrote %v", w.Synced, w.Fake.Writes)
	}
}

func TestUndoRunsAsApprovedWriteInShadowAndWhilePaused(t *testing.T) {
	w := livetest.ICloud(t, "live")
	w.Arrive("friend", []string{}...)
	id := w.Flag("friend")
	if !slices.Contains(w.InboxBox().Messages[1].Flags, flagged) {
		t.Fatal("flag not set")
	}
	shadow := livetest.ICloud(t, "shadow")
	if err := mailops.StoreFlag(shadow.Automatic(), shadow.Fake, "INBOX", 0, []uint32{1}, flagged, true); !refused(err) {
		t.Fatalf("shadow automatic write = %v", err)
	}
	if err := w.Live.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	if err := mailops.StoreFlag(w.Automatic(), w.Fake, "INBOX", 0, []uint32{1}, flagged, true); !refused(err) {
		t.Fatalf("paused automatic write = %v", err)
	}
	w.Fake.SetEnv(config.Map(map[string]string{"ATRIUM_MODE": "shadow"}))
	out, err := w.Live.Undo(t.Context(), id)
	if err != nil || !out.Undone || !reflect.DeepEqual(out.Detail, py.Dict{{Key: "messages", Value: 1}}) {
		t.Fatalf("Undo in shadow while paused = %+v, %v", out, err)
	}
	if slices.Contains(w.InboxBox().Messages[1].Flags, flagged) {
		t.Fatal("undo left the flag")
	}
	last := w.Fake.Writes[len(w.Fake.Writes)-1]
	if last.Verb != "STORE" || !reflect.DeepEqual(last.Args, []string{"-FLAGS.SILENT", flagged}) {
		t.Fatalf("unflag wrote %+v", last)
	}
}

func TestCorrectionUndoClearsTheCorrection(t *testing.T) {
	w := livetest.ICloud(t, "live")
	w.Arrive("c1", []string{}...)
	mid := w.MID("c1")
	if _, err := corrections.Record(t.Context(), w.DB, mid, "Bills", corrections.Source, w.Clock); err != nil {
		t.Fatal(err)
	}
	id := w.File("c1", live.Correct, "Bills")
	w.Sync()
	if _, err := w.Live.Undo(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if c, err := corrections.Get(t.Context(), w.DB.Read, mid); err != nil || c != nil {
		t.Fatalf("correction after undo = %+v, %v", c, err)
	}
	if len(uids(w.Fake.Boxes["Bills"])) != 0 {
		t.Fatal("not moved back")
	}
}

func TestGmailUndoRestoresInboxLabel(t *testing.T) {
	w := livetest.Gmail(t, "live")
	w.Arrive("g")
	id := w.Relabel("g", "Receipts")
	box := w.Fake.Boxes[config.GmailAllMail]
	if !reflect.DeepEqual(box.Messages[1].Labels, []string{"Receipts"}) {
		t.Fatalf("labels = %v", box.Messages[1].Labels)
	}
	if _, err := w.Live.Undo(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(box.Messages[1].Labels, []string{mailops.InboxLabel}) {
		t.Fatalf("labels after undo = %v", box.Messages[1].Labels)
	}
	if !reflect.DeepEqual(w.Synced, []string{config.Gmail}) {
		t.Fatalf("synced = %v", w.Synced)
	}
}

func TestGmailUndoAfterUIDValidityChangeFails(t *testing.T) {
	w := livetest.Gmail(t, "live")
	w.Arrive("g")
	id := w.Relabel("g", "Receipts")
	w.Fake.Boxes[config.GmailAllMail].Validity = 2
	_, err := w.Live.Undo(t.Context(), id)
	undoErr(t, err, "MailOpError: UIDVALIDITY changed")
}

func TestGmailTrashUndoUntrashes(t *testing.T) {
	w := livetest.Gmail(t, "live")
	w.Arrive("doomed")
	id := w.GmailTrash("doomed")
	trash := w.Fake.Boxes["[Gmail]/Trash"]
	trash.SetFlags(1, nil, []string{mailops.TrashLabel})
	out, err := w.Live.Undo(t.Context(), id)
	if err != nil || !out.Undone {
		t.Fatalf("Undo = %+v, %v", out, err)
	}
	if !reflect.DeepEqual(trash.Messages[1].Labels, []string{}) {
		t.Fatalf("trash labels = %v", trash.Messages[1].Labels)
	}
	trash.Validity = 6
	again, _, _ := w.Live.Begin(t.Context(), config.Gmail, 1, "x", live.Trash, nil, new("[Gmail]/Trash"), py.Dict{}, nil)
	_ = w.Live.Finish(t.Context(), again, nil, py.Dict{{Key: "op", Value: live.OpGmailUntrash}, {Key: "trash_imap", Value: "[Gmail]/Trash"},
		{Key: "dest_uidvalidity", Value: 5}, {Key: "uids", Value: []any{1}}}, nil)
	_, err = w.Live.Undo(t.Context(), again)
	undoErr(t, err, "MailOpError: Trash UIDVALIDITY changed")
}

func moveBackPlan(items ...py.Dict) py.Dict {
	list := make([]any, len(items))
	for i, it := range items {
		list[i] = it
	}
	return py.Dict{
		{Key: "op", Value: live.OpMoveBack}, {Key: "account", Value: config.ICloud}, {Key: "src_imap", Value: "INBOX"},
		{Key: "dest_imap", Value: "Bills"}, {Key: "dest_uidvalidity", Value: nil}, {Key: "items", Value: list},
	}
}

func item(id int64, msgid string, destUID any) py.Dict {
	return py.Dict{{Key: "id", Value: id}, {Key: "msgid", Value: msgid}, {Key: "uid", Value: 1}, {Key: "dest_uid", Value: destUID}}
}

func TestMoveBackFindsUnmappedMessagesByUniqueMessageID(t *testing.T) {
	w := livetest.ICloud(t, "live")
	bills := w.Fake.Boxes["Bills"]
	for _, s := range []string{"one", "dup", "dup"} {
		h, b := mailtest.RawWith(s, "a@x.test", s, "x")
		bills.Add(h, b, nil, nil)
	}
	id, _, _ := w.Live.Begin(t.Context(), config.ICloud, 1, "x", live.File, nil, new("Bills"), py.Dict{}, nil)
	plan := moveBackPlan(item(1, "one@x.test", nil), item(2, "dup@x.test", nil), item(3, "", nil), item(4, "z", 9))
	_ = w.Live.Finish(t.Context(), id, nil, plan, nil)
	out, err := w.Live.Undo(t.Context(), id)
	if err != nil || !reflect.DeepEqual(out.Detail, py.Dict{{Key: "restored", Value: 1}, {Key: "missing", Value: 3}}) {
		t.Fatalf("Undo = %+v, %v", out, err)
	}
	if got := uids(bills); !reflect.DeepEqual(got, []uint32{2, 3}) {
		t.Fatalf("Bills = %v", got)
	}

	empty, _, _ := w.Live.Begin(t.Context(), config.ICloud, 2, "y", live.File, nil, new("Bills"), py.Dict{}, nil)
	_ = w.Live.Finish(t.Context(), empty, nil, moveBackPlan(item(5, "dup@x.test", nil), item(6, "", 40)), nil)
	_, err = w.Live.Undo(t.Context(), empty)
	undoErr(t, err, "MailOpError: no message left in the destination to move back")
}

func TestMoveBackRefusesWithoutUIDPLUS(t *testing.T) {
	w := livetest.ICloud(t, "live")
	w.Arrive("m")
	id := w.File("m", live.File, "Bills")
	delete(w.Fake.Caps, "UIDPLUS")
	before := len(w.Fake.Writes)
	_, err := w.Live.Undo(t.Context(), id)
	undoErr(t, err, "MailOpError: server lacks UIDPLUS: refusing to move")
	if len(w.Fake.Writes) != before || w.Fake.RW != "INBOX" {
		t.Fatalf("a refused move wrote %v (rw %s)", w.Fake.Writes[before:], w.Fake.RW)
	}
}

func TestSyncFailureAfterUndoIsReported(t *testing.T) {
	w := livetest.ICloud(t, "live")
	w.Arrive("m", []string{}...)
	id := w.Flag("m")
	w.Registry.Sync = func(context.Context, string) error { return errors.New("IMAP down") }
	out, err := w.Live.Undo(t.Context(), id)
	if err != nil || !out.Undone || out.SyncError == nil || *out.SyncError != "RuntimeError: IMAP down" {
		t.Fatalf("Undo = %+v, %v", out, err)
	}
	w.Registry.Sync = nil
	a, _ := live.Get(t.Context(), w.DB.Read, id)
	if a.Status != live.Undone {
		t.Fatalf("status = %s", a.Status)
	}
}

func TestPauseResumeStatus(t *testing.T) {
	w := livetest.ICloud(t, "live", livetest.Seed{Subject: "old", Flags: []string{seen}})
	if w.Live.Paused() {
		t.Fatal("paused at start")
	}
	w.Arrive("m", []string{}...)
	w.Flag("m")
	if err := w.Live.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.Cfg.PausePath()); err != nil {
		t.Fatalf("pause file: %v", err)
	}
	st, err := w.Live.Status(t.Context())
	want := py.Dict{{Key: "mode", Value: "live"}, {Key: "paused", Value: true}, {Key: "golive_ts", Value: "1792080000.0"},
		{Key: "baseline", Value: int64(1)}, {Key: "actions", Value: py.Dict{{Key: "flag", Value: py.Dict{{Key: "applied", Value: int64(1)}}}}}}
	if err != nil || !reflect.DeepEqual(st, want) {
		t.Fatalf("Status = %v, %v", st, err)
	}
	if err := w.Live.SetPaused(false); err != nil || w.Live.Paused() {
		t.Fatalf("resume: %v", err)
	}
}

func TestAbsorbLabelUndoMergesEarlierPartials(t *testing.T) {
	w := livetest.Gmail(t, "live")
	for i, plan := range []py.Dict{
		{{Key: "add", Value: []string{`\Inbox`}}, {Key: "remove", Value: []string{"A"}}},
		{{Key: "add", Value: []string{`\Inbox`, "B"}}, {Key: "remove", Value: []string{"A", "C"}}},
	} {
		id, _, _ := w.Live.Begin(t.Context(), config.Gmail, 7, "d", live.File, nil, new("x"+strconv.Itoa(i)), py.Dict{}, nil)
		_ = w.Live.Partial(t.Context(), id, "partial: NO", py.Dict{}, plan.Set("op", live.OpGmailLabels))
	}
	add, remove, err := w.Live.AbsorbLabelUndo(t.Context(), 7, live.File)
	if err != nil || !reflect.DeepEqual(add, []string{`\Inbox`, "B"}) || !reflect.DeepEqual(remove, []string{"A", "C"}) {
		t.Fatalf("AbsorbLabelUndo = %v %v %v", add, remove, err)
	}
	for _, a := range w.Actions() {
		if a.Undo != nil || a.Undoable {
			t.Fatalf("absorbed row kept its undo: %+v", a)
		}
	}
	add, remove, _ = w.Live.AbsorbLabelUndo(t.Context(), 7, live.File)
	if len(add) != 0 || len(remove) != 0 {
		t.Fatal("absorbed twice")
	}
}

func TestListCountSkipHold(t *testing.T) {
	w := livetest.ICloud(t, "live")
	w.Arrive("a", []string{}...)
	w.Arrive("b")
	flag := w.Flag("a")
	if err := w.Live.Skip(t.Context(), config.ICloud, w.MID("b"), "d", live.File, new("INBOX"), new("Bills"), "unread", new(int64(3))); err != nil {
		t.Fatal(err)
	}
	held, _, _ := w.Live.Begin(t.Context(), config.ICloud, w.MID("b"), "d", live.Trash, nil, nil, py.Dict{}, nil)
	if err := w.Live.Hold(t.Context(), held, "copied but not expunged: x", py.Dict{{Key: "uid", Value: 2}}); err != nil {
		t.Fatal(err)
	}
	all, err := live.List(t.Context(), w.DB.Read, "", "", live.DefaultLimit, 0)
	if err != nil || len(all) != 3 || all[0].ID != held || all[2].ID != flag {
		t.Fatalf("List = %+v, %v", all, err)
	}
	if all[0].Status != live.Held || *all[0].After != `{"uid": 2}` || all[1].Status != live.Skipped ||
		*all[1].Error != "skipped: unread" || *all[1].DecisionID != 3 || all[1].Undoable || *all[2].Subject != "a" {
		t.Fatalf("shaped rows = %+v", all)
	}
	trash, _ := live.List(t.Context(), w.DB.Read, "", live.Trash, 10, 0)
	paged, _ := live.List(t.Context(), w.DB.Read, config.ICloud, "", 1, 1)
	none, _ := live.List(t.Context(), w.DB.Read, config.Gmail, "", 10, 0)
	if len(trash) != 1 || len(paged) != 1 || paged[0].ID != all[1].ID || none == nil || len(none) != 0 {
		t.Fatalf("filters: %v %v %v", trash, paged, none)
	}
	for _, c := range []struct {
		account, kind string
		want          int64
	}{{"", "", 3}, {config.ICloud, live.File, 1}, {config.Gmail, "", 0}} {
		if n, err := live.Count(t.Context(), w.DB.Read, c.account, c.kind); err != nil || n != c.want {
			t.Fatalf("Count(%q, %q) = %d, %v", c.account, c.kind, n, err)
		}
	}
	if a, err := live.Get(t.Context(), w.DB.Read, 999); a != nil || err != nil {
		t.Fatalf("Get(999) = %v, %v", a, err)
	}
}

func refused(err error) bool {
	var r safety.WriteRefused
	return errors.As(err, &r)
}

func TestUndoWithoutRegistryHasNoHandler(t *testing.T) {
	w := livetest.ICloud(t, "live")
	w.Arrive("m", []string{}...)
	id := w.Flag("m")
	bare := &live.Live{DB: w.DB, Cfg: w.Cfg}
	_, err := bare.Undo(t.Context(), id)
	undoErr(t, err, "no undo handler for 'unflag'")
}
