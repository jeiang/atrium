package executors_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/executors"
	"github.com/jeiang/atrium/internal/proposals/handlers"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/safety"
)

type world struct {
	t        *testing.T
	account  string
	db       *db.DB
	fake     *mailtest.FakeClient
	pipeline *proposals.Pipeline
	registry *proposals.Registry
	synced   []string
	clock    float64
}

func newWorld(t *testing.T, account string, setup func(*mailtest.FakeClient)) *world {
	t.Helper()
	dir := t.TempDir()
	env := config.Map(map[string]string{
		"ATRIUM_MODE":      config.Shadow,
		"ATRIUM_DB":        filepath.Join(dir, "atrium.db"),
		"ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"),
	})
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, account: account, db: dbtest.Schema(t), fake: mailtest.NewFakeClient(env), clock: 1_792_080_000}
	setup(w.fake)
	clock := func() float64 { return w.clock }
	syncer := &mailsync.Syncer{DB: w.db, Cfg: cfg, Now: clock}
	if _, err := syncer.Account(context.Background(), account, w.fake); err != nil {
		t.Fatal(err)
	}
	open := func(context.Context, string) (mail.Client, error) { return w.fake, nil }
	w.registry = proposals.NewRegistry()
	handlers.Register(w.registry, handlers.Deps{DB: w.db, Open: open, Syncer: syncer, Now: clock})
	inner := w.registry.Sync
	w.registry.Sync = func(ctx context.Context, account string) error {
		w.synced = append(w.synced, account)
		return inner(ctx, account)
	}
	w.pipeline = &proposals.Pipeline{DB: w.db, Registry: w.registry, Now: clock}
	return w
}

func icloudWorld(t *testing.T, caps ...string) *world {
	return newWorld(t, config.ICloud, func(f *mailtest.FakeClient) {
		f.AddBox(config.Inbox, mailtest.NewBox(1))
		bills := f.AddBox("Bills", mailtest.NewBox(3))
		f.AddBox("Deleted Messages", mailtest.NewBox(9), `\Trash`)
		for i := range 4 {
			header, text := mailtest.RawWith(fmt.Sprintf("invoice %d", i), "shop@bills.test", fmt.Sprintf("m%d", i), "hello")
			bills.Add(header, text, nil, nil)
		}
		if caps != nil {
			f.Caps = map[string]bool{}
			for _, c := range caps {
				f.Caps[c] = true
			}
		}
	})
}

func gmailWorld(t *testing.T, labelled ...[]string) *world {
	return newWorld(t, config.Gmail, func(f *mailtest.FakeClient) {
		f.Gmail = true
		all := f.AddBox(config.GmailAllMail, mailtest.NewBox(1))
		f.AddBox("[Gmail]/Trash", mailtest.NewBox(5), `\Trash`)
		for i, labels := range labelled {
			header, text := mailtest.RawWith(fmt.Sprintf("note %d", i), "a@gm.test", fmt.Sprintf("g%d", i), "hello")
			all.Add(header, text, nil, labels)
		}
	})
}

func (w *world) ids(query string, args ...any) []int64 {
	w.t.Helper()
	rows, err := w.db.Read.Query(query, args...)
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		err := r.Scan(&id)
		return id, err
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *world) exec(query string, args ...any) {
	w.t.Helper()
	if _, err := w.db.Write.Exec(query, args...); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) messageIDs(mailbox string) []int64 {
	w.t.Helper()
	return w.ids("SELECT m.id FROM messages m JOIN mailboxes b ON b.id = m.mailbox_id WHERE b.name = ? AND m.gone_at IS NULL ORDER BY m.id", mailbox)
}

func (w *world) snapshots(ids []int64) []any {
	w.t.Helper()
	snaps, err := mailops.Snapshots(context.Background(), w.db.Read, ids)
	if err != nil {
		w.t.Fatal(err)
	}
	out := make([]any, len(snaps))
	for i, s := range snaps {
		out[i] = s.Dict()
	}
	return out
}

func (w *world) propose(kind string, payload py.Dict) int64 {
	w.t.Helper()
	account := w.account
	id, ok, err := proposals.Create(context.Background(), w.db, kind, &account, "title", payload, py.Dict{}, "fp", w.clock)
	if err != nil || !ok {
		w.t.Fatalf("create = %v, %v", ok, err)
	}
	return id
}

func (w *world) splitPayload(ids []int64, dest string) py.Dict {
	return py.Dict{{Key: "folder", Value: "Bills"}, {Key: "groups", Value: []any{
		py.Dict{{Key: "dest", Value: dest}, {Key: "messages", Value: w.snapshots(ids)}},
	}}}
}

func (w *world) apply(id int64, selection any) *proposals.Proposal {
	w.t.Helper()
	p, err := w.pipeline.ApproveAndApply(context.Background(), id, selection)
	if err != nil {
		w.t.Fatal(err)
	}
	return p
}

func (w *world) undo(id int64) proposals.UndoResult {
	w.t.Helper()
	out, err := w.pipeline.Undo(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *world) actions(id int64) []proposals.Action {
	w.t.Helper()
	out, err := proposals.Actions(context.Background(), w.db.Read, id)
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

func field(v any, key string) any {
	d, _ := v.(py.Dict)
	out, _ := d.Get(key)
	return out
}

func verbs(f *mailtest.FakeClient) []string {
	var out []string
	for _, w := range f.Writes {
		out = append(out, w.Verb)
	}
	return out
}

func count(t *testing.T, d *db.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := d.Read.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSplitMovesWithUIDExpungeCreatesFolderAndLogsUndo(t *testing.T) {
	w := icloudWorld(t)
	ids := w.messageIDs("Bills")
	pid := w.propose(executors.RebalanceSplit, w.splitPayload(ids, "Bills/Shop"))
	done := w.apply(pid, nil)
	if done.Status != proposals.Applied || field(done.Result, "moved") != int64(4) {
		t.Fatalf("applied = %s %v", done.Status, done.Result)
	}
	if got := verbs(w.fake); !slices.Equal(got, []string{"CREATE", "SUBSCRIBE", "COPY", "STORE", "EXPUNGE"}) {
		t.Fatalf("writes = %v", got)
	}
	store, expunge := w.fake.Writes[3], w.fake.Writes[4]
	if !slices.Equal(store.Args, []string{"+FLAGS.SILENT", `\Deleted`}) || !slices.Equal(expunge.UIDs, store.UIDs) {
		t.Fatalf("store = %+v, expunge = %+v", store, expunge)
	}
	if len(w.fake.Boxes["Bills"].Messages) != 0 || len(w.fake.Boxes["Bills/Shop"].Messages) != 4 {
		t.Fatal("messages did not move")
	}
	var move, create *proposals.Action
	for _, a := range w.actions(pid) {
		switch field(a.Action, "op") {
		case "move":
			move = &a
		case "create_folder":
			create = &a
		}
	}
	if move == nil || field(move.Undo, "op") != "move_back" || field(move.Undo, "dest_uidvalidity") != int64(7) {
		t.Fatalf("move = %+v", move)
	}
	for _, item := range field(move.Undo, "items").([]any) {
		if field(item, "dest_uid") == nil || field(item, "msgid") == "" {
			t.Fatalf("undo item = %v", item)
		}
	}
	if create == nil || create.Undo == nil {
		t.Fatal("create_folder has no undo")
	}
	if n := count(t, w.db, "SELECT COUNT(*) FROM messages m JOIN mailboxes b ON b.id = m.mailbox_id WHERE b.name = 'Bills/Shop' AND m.gone_at IS NULL"); n != 4 {
		t.Fatalf("synced Bills/Shop = %d", n)
	}
	if !slices.Equal(w.synced, []string{config.ICloud}) {
		t.Fatalf("synced = %v", w.synced)
	}
}

func TestGateStillRefusesOutsideApprovedWrites(t *testing.T) {
	w := icloudWorld(t)
	ctx := context.Background()
	var refused safety.WriteRefused
	if _, err := mailops.EnsureFolder(ctx, w.fake, "Nope"); !errors.As(err, &refused) {
		t.Fatalf("unapproved create = %v", err)
	}
	if _, err := mailops.EnsureFolder(safety.ApprovedWrites(ctx, "1"), w.fake, "Nope"); err != nil {
		t.Fatal(err)
	}
	if err := w.fake.SelectRW(ctx, "Bills"); !errors.As(err, &refused) {
		t.Fatalf("unapproved select = %v", err)
	}
}

func TestApproveAndUndoRunAsTheApprovedProposalOnly(t *testing.T) {
	w := icloudWorld(t)
	pid := w.propose("probe", py.Dict{})
	var seen []string
	w.registry.Executors["probe"] = func(ctx context.Context, p *proposals.Proposal, _ any) (py.Dict, error) {
		id, ok := safety.ApprovedProposal(ctx)
		seen = append(seen, fmt.Sprintf("%s %v", id, ok))
		_, err := proposals.RecordAction(ctx, w.db.Write, p.ID, py.Dict{{Key: "op", Value: "probe"}}, py.Dict{{Key: "op", Value: "probe"}}, true, nil, w.clock)
		return py.Dict{}, err
	}
	w.registry.Undoers["probe"] = func(ctx context.Context, _ json.RawMessage) (py.Dict, error) {
		id, ok := safety.ApprovedProposal(ctx)
		seen = append(seen, fmt.Sprintf("%s %v", id, ok))
		return nil, nil
	}
	ctx := context.Background()
	if _, ok := safety.ApprovedProposal(ctx); ok {
		t.Fatal("approved outside the pipeline")
	}
	w.apply(pid, nil)
	w.undo(pid)
	want := strconv.FormatInt(pid, 10) + " true"
	if !slices.Equal(seen, []string{want, want}) {
		t.Fatalf("scopes = %v", seen)
	}
}

func TestUndoRestoresMessagesAndRemovesCreatedFolder(t *testing.T) {
	w := icloudWorld(t)
	ids := w.messageIDs("Bills")
	pid := w.propose(executors.RebalanceSplit, w.splitPayload(ids, "Bills/Shop"))
	w.apply(pid, nil)
	if p, _ := proposals.Get(context.Background(), w.db.Read, pid); !p.Undoable {
		t.Fatal("applied split is not undoable")
	}
	out := w.undo(pid)
	if !out.Undone || len(out.Outcomes) != 2 {
		t.Fatalf("undo = %+v", out)
	}
	if len(w.fake.Boxes["Bills"].Messages) != 4 || !slices.Contains(w.fake.Deleted, "Bills/Shop") {
		t.Fatal("undo did not restore")
	}
	if p, _ := proposals.Get(context.Background(), w.db.Read, pid); p.Undoable || field(p.Result, "undone_ts") != w.clock {
		t.Fatalf("after undo = %+v", p)
	}
	if got := w.messageIDs("Bills"); len(got) != 4 {
		t.Fatalf("synced Bills = %v", got)
	}
}

func TestChangedMessagesAreSkippedBySnapshotVerification(t *testing.T) {
	w := icloudWorld(t)
	ids := w.messageIDs("Bills")
	pid := w.propose(executors.RebalanceSplit, w.splitPayload(ids, "Bills/Shop"))
	w.exec("UPDATE messages SET digest = 'other' WHERE id = ?", ids[0])
	w.exec("UPDATE messages SET gone_at = 1 WHERE id = ?", ids[1])
	var uid uint32
	if err := w.db.Read.QueryRow("SELECT uid FROM messages WHERE id = ?", ids[2]).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	w.fake.Boxes["Bills"].Messages[uid].Header, _ = mailtest.RawWith("x", "a@x.test", "different", "")
	done := w.apply(pid, nil)
	var reasons []string
	for _, s := range field(done.Result, "skipped").([]any) {
		reasons = append(reasons, field(s, "reason").(string))
	}
	slices.Sort(reasons)
	if field(done.Result, "moved") != int64(1) || !slices.Equal(reasons, []string{"changed", "gone", "msgid"}) {
		t.Fatalf("result = %v", done.Result)
	}
	if len(w.fake.Boxes["Bills"].Messages) != 3 {
		t.Fatal("skipped messages moved")
	}
	skips := 0
	for _, a := range w.actions(pid) {
		if !a.OK && field(a.Action, "op") == "skip" && *a.Error == "changed since the proposal" {
			skips++
		}
	}
	if skips != 1 {
		t.Fatalf("skip actions = %d", skips)
	}
}

func TestUIDValidityChangeSkipsEverything(t *testing.T) {
	w := icloudWorld(t)
	pid := w.propose(executors.RebalanceSplit, w.splitPayload(w.messageIDs("Bills"), "Bills/Shop"))
	w.fake.Boxes["Bills"].Validity = 99
	done := w.apply(pid, nil)
	for _, s := range field(done.Result, "skipped").([]any) {
		if field(s, "reason") != "uidvalidity" {
			t.Fatalf("skip = %v", s)
		}
	}
	if field(done.Result, "moved") != int64(0) || slices.Contains(verbs(w.fake), "COPY") {
		t.Fatalf("result = %v, writes = %v", done.Result, verbs(w.fake))
	}
}

func TestMoveRefusedWithoutUIDPLUS(t *testing.T) {
	w := icloudWorld(t, "IDLE")
	pid := w.propose(executors.RebalanceSplit, w.splitPayload(w.messageIDs("Bills"), "Bills"))
	done := w.apply(pid, nil)
	if done.Status != proposals.Failed || field(done.Result, "error") != "MailOpError: server lacks UIDPLUS: refusing to move" {
		t.Fatalf("result = %s %v", done.Status, done.Result)
	}
	for _, v := range verbs(w.fake) {
		if v == "COPY" || v == "STORE" || v == "EXPUNGE" {
			t.Fatalf("writes = %v", verbs(w.fake))
		}
	}
	if !slices.Equal(w.synced, []string{config.ICloud}) {
		t.Fatalf("a failed apply did not resync: %v", w.synced)
	}
}

func selectionErr(t *testing.T, err error, want string) {
	t.Helper()
	var sel proposals.SelectionError
	var prop proposals.ProposalError
	if !errors.As(err, &sel) || err.Error() != want || !errors.As(err, &prop) {
		t.Fatalf("err = %v, want SelectionError %q", err, want)
	}
}

func TestSelectionSubsetAndRenameAndInvalid(t *testing.T) {
	w := icloudWorld(t)
	ids := w.messageIDs("Bills")
	pid := w.propose(executors.RebalanceSplit, w.splitPayload(ids, "Bills/Shop"))
	ctx := context.Background()
	groups := func(picks ...py.Dict) py.Dict {
		list := make([]any, len(picks))
		for i, p := range picks {
			list[i] = p
		}
		return py.Dict{{Key: "groups", Value: list}}
	}
	cases := []struct {
		selection py.Dict
		want      string
	}{
		{groups(py.Dict{{Key: "name", Value: "INBOX"}}), "invalid folder name 'INBOX'"},
		{groups(py.Dict{{Key: "name", Value: " inbox "}}), "invalid folder name ' inbox '"},
		{groups(py.Dict{{Key: "name", Value: "a|b"}}), "invalid folder name 'a|b'"},
		{groups(py.Dict{{Key: "name", Value: "a\x01b"}}), `invalid folder name 'a\x01b'`},
		{groups(py.Dict{{Key: "message_ids", Value: []any{int64(10_000)}}}), "selection names messages outside the proposal"},
		{groups(), "selection does not match the proposal groups"},
		{groups(py.Dict{}, py.Dict{}), "selection does not match the proposal groups"},
	}
	for _, c := range cases {
		_, err := w.pipeline.ApproveAndApply(ctx, pid, c.selection)
		selectionErr(t, err, c.want)
	}
	p, _ := proposals.Get(ctx, w.db.Read, pid)
	if p.Status != proposals.Pending || p.DecidedTS != nil || len(w.fake.Writes) != 0 {
		t.Fatalf("after selection errors = %+v, writes %v", p, w.fake.Writes)
	}
	done := w.apply(pid, groups(py.Dict{{Key: "name", Value: "Bills/Renamed"}, {Key: "message_ids", Value: []any{ids[0], strconv.FormatInt(ids[1], 10)}}}))
	if field(done.Result, "moved") != int64(2) || len(w.fake.Boxes["Bills/Renamed"].Messages) != 2 || len(w.fake.Boxes["Bills"].Messages) != 2 {
		t.Fatalf("renamed subset = %v", done.Result)
	}
	if !reflect.DeepEqual(field(done.Result, "destinations"), []any{"Bills/Renamed"}) {
		t.Fatalf("destinations = %v", field(done.Result, "destinations"))
	}
}

func TestValidName(t *testing.T) {
	long := ""
	for range executors.MaxNameChars {
		long += "é"
	}
	for value, want := range map[string]string{"  Bills/Shop \t": "Bills/Shop", long: long} {
		if got, err := executors.ValidName(value); err != nil || got != want {
			t.Errorf("ValidName(%q) = %q, %v", value, got, err)
		}
	}
	for _, value := range []string{"", "   ", long + "x", "Inbox", "a|b", "a\nb"} {
		if _, err := executors.ValidName(value); err == nil {
			t.Errorf("ValidName(%q) accepted", value)
		}
	}
}

func TestLabelMergeRelabelsDeletesEmptyLabelAndUndoReadds(t *testing.T) {
	w := gmailWorld(t, []string{"Parent/Small"}, []string{"Parent/Small", "Other"}, []string{"Parent"})
	ids := w.ids("SELECT id FROM messages WHERE labels LIKE '%Parent/Small%' ORDER BY id")
	payload := py.Dict{{Key: "label", Value: "Parent/Small"}, {Key: "into", Value: "Parent"}, {Key: "messages", Value: w.snapshots(ids)}}
	pid := w.propose(executors.GmailLabelMerge, payload)
	done := w.apply(pid, nil)
	if field(done.Result, "relabelled") != int64(2) || field(done.Result, "label_deleted") != true {
		t.Fatalf("result = %v", done.Result)
	}
	if !slices.Equal(w.fake.Deleted, []string{mail.UTF7Encode("Parent/Small")}) {
		t.Fatalf("deleted = %v", w.fake.Deleted)
	}
	ops := map[string]bool{}
	for _, wr := range w.fake.Writes {
		if wr.Verb == "STORE" {
			ops[wr.Args[0]] = true
			if wr.Args[1] != "Parent" && wr.Args[1] != "Parent/Small" {
				t.Fatalf("store = %+v", wr)
			}
		}
	}
	if !ops["+X-GM-LABELS"] || !ops["-X-GM-LABELS"] || len(ops) != 2 {
		t.Fatalf("stores = %v", ops)
	}
	labels := func() [][]string {
		var out [][]string
		box := w.fake.Boxes[config.GmailAllMail]
		for _, uid := range box.UIDs() {
			out = append(out, slices.Sorted(slices.Values(box.Messages[uid].Labels)))
		}
		return out
	}
	if got := labels(); !reflect.DeepEqual(got, [][]string{{"Parent"}, {"Other", "Parent"}, {"Parent"}}) {
		t.Fatalf("labels = %v", got)
	}
	if out := w.undo(pid); !out.Undone {
		t.Fatalf("undo = %+v", out)
	}
	if got := labels(); !reflect.DeepEqual(got, [][]string{{"Parent/Small"}, {"Other", "Parent/Small"}, {"Parent"}}) {
		t.Fatalf("labels after undo = %v", got)
	}
}

func TestLabelKeptWhenNotEmptyAfterRelabel(t *testing.T) {
	w := gmailWorld(t, []string{"Parent/Small"}, []string{"Parent/Small"})
	ids := w.ids("SELECT id FROM messages ORDER BY id")
	payload := py.Dict{{Key: "label", Value: "Parent/Small"}, {Key: "into", Value: "Parent"}, {Key: "messages", Value: w.snapshots(ids)}}
	pid := w.propose(executors.GmailLabelMerge, payload)
	w.exec("UPDATE messages SET digest = 'other' WHERE id = ?", ids[0])
	done := w.apply(pid, nil)
	if field(done.Result, "relabelled") != int64(1) || field(done.Result, "label_deleted") != false || len(w.fake.Deleted) != 0 {
		t.Fatalf("result = %v, deleted %v", done.Result, w.fake.Deleted)
	}
	if field(done.Result, "summary") != "1 relabelled, label kept, 1 skipped as changed" {
		t.Fatalf("summary = %v", field(done.Result, "summary"))
	}
}

func deletionPayload(w *world, ids []int64, account string) py.Dict {
	rule := py.Dict{
		{Key: "id", Value: "del-test"}, {Key: "account", Value: account}, {Key: "action", Value: "delete"},
		{Key: "when", Value: []any{py.Dict{{Key: "field", Value: "from_addr"}, {Key: "op", Value: "eq"}, {Key: "value", Value: "shop@bills.test"}}}},
	}
	return py.Dict{{Key: "rule", Value: rule}, {Key: "messages", Value: w.snapshots(ids)}}
}

func TestDeletionRuleCreatesRuleTrashesBacklogAndUndo(t *testing.T) {
	w := icloudWorld(t)
	ids := w.messageIDs("Bills")
	pid := w.propose(executors.DeletionRule, deletionPayload(w, ids, config.ICloud))
	done := w.apply(pid, py.Dict{{Key: "message_ids", Value: []any{ids[0], ids[1], ids[2]}}})
	if field(done.Result, "trashed") != int64(3) || field(done.Result, "summary") != "rule created, 3 moved to Trash, 0 skipped as changed" {
		t.Fatalf("result = %v", done.Result)
	}
	var action string
	if err := w.db.Read.QueryRow("SELECT action FROM rules WHERE id = 'del-test'").Scan(&action); err != nil || action != "delete" {
		t.Fatalf("rule = %q, %v", action, err)
	}
	if len(w.fake.Boxes["Deleted Messages"].Messages) != 3 || len(w.fake.Boxes["Bills"].Messages) != 1 {
		t.Fatal("backlog not trashed")
	}
	if out := w.undo(pid); !out.Undone || len(w.fake.Boxes["Bills"].Messages) != 4 {
		t.Fatalf("undo = %+v", out)
	}
	if n := count(t, w.db, "SELECT COUNT(*) FROM rules"); n != 0 {
		t.Fatalf("rules after undo = %d", n)
	}
}

func TestDeletionWithExistingRuleOnlyTrashes(t *testing.T) {
	w := icloudWorld(t)
	ids := w.messageIDs("Bills")
	w.exec(`INSERT INTO rules (position, id, account, action, dest, conditions) VALUES (0, 'del-test', 'icloud', 'delete', NULL, '[]')`)
	pid := w.propose(executors.DeletionRule, deletionPayload(w, ids, config.ICloud))
	done := w.apply(pid, nil)
	if field(done.Result, "summary") != "rule already present, 4 moved to Trash, 0 skipped as changed" {
		t.Fatalf("result = %v", done.Result)
	}
	for _, a := range w.actions(pid) {
		if field(a.Action, "op") == "rule_create" {
			t.Fatal("existing rule recorded as created")
		}
	}
}

func TestGmailDeleteCopiesToTrashAndUndoRemovesTrashLabel(t *testing.T) {
	w := gmailWorld(t, []string{"X"}, []string{"X"})
	ids := w.ids("SELECT id FROM messages ORDER BY id")
	pid := w.propose(executors.DeletionRule, deletionPayload(w, ids, config.Gmail))
	done := w.apply(pid, nil)
	if field(done.Result, "trashed") != int64(2) || len(w.fake.Boxes["[Gmail]/Trash"].Messages) != 2 {
		t.Fatalf("result = %v", done.Result)
	}
	if out := w.undo(pid); !out.Undone {
		t.Fatalf("undo = %+v", out)
	}
	var last mailtest.FakeWrite
	for _, wr := range w.fake.Writes {
		if wr.Verb == "STORE" {
			last = wr
		}
	}
	if !slices.Equal(last.Args, []string{"-X-GM-LABELS", `\Trash`}) {
		t.Fatalf("last store = %+v", last)
	}
}

func TestFailedApplyMarksFailedAndCanReapply(t *testing.T) {
	w := icloudWorld(t)
	pid := w.propose(executors.RebalanceSplit, w.splitPayload(w.messageIDs("Bills"), "Bills/Shop"))
	w.fake.Caps = map[string]bool{}
	if p := w.apply(pid, nil); p.Status != proposals.Failed {
		t.Fatalf("status = %s", p.Status)
	}
	w.fake.Caps = map[string]bool{"UIDPLUS": true}
	if p := w.apply(pid, nil); p.Status != proposals.Applied {
		t.Fatalf("status = %s, %v", p.Status, p.Result)
	}
}

func TestPartialMoveIsLoggedThenFails(t *testing.T) {
	w := icloudWorld(t)
	w.fake.AddBox("Elsewhere", mailtest.NewBox(4))
	pid := w.propose(executors.RebalanceMerge, py.Dict{{Key: "folder", Value: "Bills"}, {Key: "groups", Value: []any{
		py.Dict{{Key: "dest", Value: "Elsewhere"}, {Key: "messages", Value: w.snapshots(w.messageIDs("Bills"))}},
	}}})
	w.fake.FailWrite = func(fw mailtest.FakeWrite) error {
		if fw.Verb == "EXPUNGE" {
			return errors.New("try later")
		}
		return nil
	}
	done := w.apply(pid, nil)
	if done.Status != proposals.Failed || field(done.Result, "error") != "PartialMove: copied but not expunged: try later" {
		t.Fatalf("result = %s %v", done.Status, done.Result)
	}
	logged := w.actions(pid)
	last := logged[len(logged)-1]
	copied, _ := field(last.Action, "copied").(py.Dict)
	if last.OK || *last.Error != "copied but not expunged: try later" || len(copied) != 4 || copied[0].Key != "1" ||
		field(last.Action, "dest_uidvalidity") != int64(4) {
		t.Fatalf("partial action = %+v", last)
	}
}

func TestPayloadSurvivesJSONRoundtrip(t *testing.T) {
	w := icloudWorld(t)
	pid := w.propose(executors.RebalanceSplit, w.splitPayload(w.messageIDs("Bills"), "Bills/Shop"))
	var text string
	if err := w.db.Read.QueryRow("SELECT payload FROM proposals WHERE id = ?", pid).Scan(&text); err != nil {
		t.Fatal(err)
	}
	stored, err := py.JSONLoads([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	first := field(field(stored, "groups").([]any)[0], "messages").([]any)[0]
	if field(first, "imap_name") != "Bills" {
		t.Fatalf("stored = %v", stored)
	}
}

func TestSelectionOfDeletionAndLabelMergeMustStayInsideTheProposal(t *testing.T) {
	outside := py.Dict{{Key: "message_ids", Value: []any{int64(10_000)}}}
	icloud := icloudWorld(t)
	pid := icloud.propose(executors.DeletionRule, deletionPayload(icloud, icloud.messageIDs("Bills"), config.ICloud))
	_, err := icloud.pipeline.ApproveAndApply(context.Background(), pid, outside)
	selectionErr(t, err, "selection names messages outside the proposal")
	if n := count(t, icloud.db, "SELECT COUNT(*) FROM rules"); n != 0 || len(icloud.fake.Writes) != 0 {
		t.Fatalf("a refused selection wrote: rules %d, writes %v", n, icloud.fake.Writes)
	}
	gmail := gmailWorld(t, []string{"Parent/Small"})
	ids := gmail.ids("SELECT id FROM messages ORDER BY id")
	pid = gmail.propose(executors.GmailLabelMerge, py.Dict{{Key: "label", Value: "Parent/Small"}, {Key: "into", Value: "Parent"}, {Key: "messages", Value: gmail.snapshots(ids)}})
	_, err = gmail.pipeline.ApproveAndApply(context.Background(), pid, outside)
	selectionErr(t, err, "selection names messages outside the proposal")
	if p, _ := proposals.Get(context.Background(), gmail.db.Read, pid); p.Status != proposals.Pending {
		t.Fatalf("status = %s", p.Status)
	}
}

func TestSelectionIDsFollowPythonInt(t *testing.T) {
	w := icloudWorld(t)
	ids := w.messageIDs("Bills")
	pid := w.propose(executors.DeletionRule, deletionPayload(w, ids, config.ICloud))
	for _, bad := range []string{"+_1", "-_1"} {
		done := w.apply(pid, py.Dict{{Key: "message_ids", Value: []any{bad}}})
		if done.Status != proposals.Failed || field(done.Result, "error") != "ValueError: invalid literal for int() with base 10: "+py.Repr(bad) {
			t.Fatalf("%q: %s %v", bad, done.Status, done.Result)
		}
	}
	if n := count(t, w.db, "SELECT COUNT(*) FROM rules"); n != 0 || len(w.fake.Writes) != 0 {
		t.Fatalf("a malformed selection wrote: rules %d, writes %v", n, w.fake.Writes)
	}
	arabic := ""
	for _, r := range strconv.FormatInt(ids[0], 10) {
		arabic += string('\u0660' + r - '0')
	}
	done := w.apply(pid, py.Dict{{Key: "message_ids", Value: []any{arabic, " " + strconv.FormatInt(ids[1], 10) + " "}}})
	if field(done.Result, "trashed") != int64(2) {
		t.Fatalf("Unicode and padded ids = %s %v", done.Status, done.Result)
	}
}

func TestNonObjectSelectionFailsTheProposalLikePythonsGet(t *testing.T) {
	proposeMoves := func(w *world) int64 {
		return w.propose(executors.InboxBacklog, py.Dict{{Key: "folder", Value: "INBOX"}, {Key: "groups", Value: []any{}}})
	}
	proposeDeletion := func(w *world) int64 {
		return w.propose(executors.DeletionRule, deletionPayload(w, w.messageIDs("Bills"), config.ICloud))
	}
	for name, selection := range map[string]any{"list": []any{int64(1)}, "int": int64(3), "str": "x", "true": true, "float": 1.5} {
		for kind, propose := range map[string]func(*world) int64{"moves": proposeMoves, "deletion": proposeDeletion} {
			w := icloudWorld(t)
			done := w.apply(propose(w), selection)
			want := "AttributeError: '" + py.TypeName(selection) + "' object has no attribute 'get'"
			if done.Status != proposals.Failed || field(done.Result, "error") != want {
				t.Fatalf("%s %s: %s %v, want %q", kind, name, done.Status, done.Result, want)
			}
			if n := count(t, w.db, "SELECT COUNT(*) FROM rules"); n != 0 || len(w.fake.Writes) != 0 {
				t.Fatalf("%s %s wrote: rules %d, writes %v", kind, name, n, w.fake.Writes)
			}
		}
	}
	for _, falsy := range []any{nil, py.Dict{}, []any{}, int64(0), "", false} {
		w := icloudWorld(t)
		if done := w.apply(proposeMoves(w), falsy); done.Status != proposals.Applied {
			t.Fatalf("falsy selection %#v: %s %v", falsy, done.Status, done.Result)
		}
	}
}
