package executors

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/ruleedit"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
)

const (
	RebalanceSplit  = "rebalance_split"
	RebalanceMerge  = "rebalance_merge"
	DeletionRule    = "deletion_rule"
	GmailLabelMerge = "gmail_label_merge"
	InboxBacklog    = "inbox_backlog"

	OpDeleteFolder = "delete_folder"
	OpRuleDelete   = "rule_delete"
)

func chosenSnapshots(messages []mailops.Snapshot, wanted any) ([]mailops.Snapshot, error) {
	if wanted == nil {
		return slices.Clone(messages), nil
	}
	items, err := py.Iter(wanted)
	if err != nil {
		return nil, err
	}
	ids := map[int64]bool{}
	outside := false
	for _, item := range items {
		id, err := py.Int(item)
		if err != nil {
			return nil, err
		}
		if id.IsInt64() {
			ids[id.Int64()] = true
		} else {
			outside = true
		}
	}
	known := map[int64]bool{}
	for _, m := range messages {
		known[m.ID] = true
	}
	for id := range ids {
		outside = outside || !known[id]
	}
	if outside {
		return nil, proposals.SelectionError("selection names messages outside the proposal")
	}
	var out []mailops.Snapshot
	for _, m := range messages {
		if ids[m.ID] {
			out = append(out, m)
		}
	}
	return out, nil
}

type group struct {
	Dest     string             `json:"dest"`
	Messages []mailops.Snapshot `json:"messages"`
}

type planned struct {
	name  string
	snaps []mailops.Snapshot
}

func get(d py.Dict, key string) any {
	v, _ := d.Get(key)
	return v
}

func selected(selection any, key string) (any, error) {
	if !py.Truthy(selection) {
		return nil, nil
	}
	d, ok := selection.(py.Dict)
	if !ok {
		return nil, py.AttributeError(selection, "get")
	}
	return get(d, key), nil
}

func resolveGroups(groups []group, selection any) ([]planned, error) {
	picksV, err := selected(selection, "groups")
	if err != nil {
		return nil, err
	}
	var picks []any
	if picksV != nil {
		list, err := py.Iter(picksV)
		if err != nil {
			return nil, err
		}
		if len(list) != len(groups) {
			return nil, proposals.SelectionError("selection does not match the proposal groups")
		}
		picks = list
	}
	plan := make([]planned, 0, len(groups))
	for i, g := range groups {
		pick := py.Dict{}
		if picks != nil {
			d, ok := picks[i].(py.Dict)
			if !ok {
				return nil, py.AttributeError(picks[i], "get")
			}
			pick = d
		}
		value := g.Dest
		if chosen := get(pick, "name"); py.Truthy(chosen) {
			s, ok := chosen.(string)
			if !ok {
				return nil, py.AttributeError(chosen, "strip")
			}
			value = s
		}
		name, err := ValidName(value)
		if err != nil {
			return nil, err
		}
		snaps, err := chosenSnapshots(g.Messages, get(pick, "message_ids"))
		if err != nil {
			return nil, err
		}
		plan = append(plan, planned{name, snaps})
	}
	return plan, nil
}

type Executors struct {
	DB   *db.DB
	Open mailops.Opener
	Now  func() float64
}

func (e *Executors) now() float64 {
	if e.Now != nil {
		return e.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (e *Executors) record(ctx context.Context, pid int64, action, undo py.Dict) error {
	var plan any
	if undo != nil {
		plan = undo
	}
	_, err := proposals.RecordAction(ctx, e.DB.Write, pid, action, plan, true, nil, e.now())
	return err
}

func (e *Executors) recordFailed(ctx context.Context, pid int64, action py.Dict, text string) error {
	_, err := proposals.RecordAction(ctx, e.DB.Write, pid, action, nil, false, &text, e.now())
	return err
}

func skipList(skipped []mailops.Skip) []any {
	out := make([]any, len(skipped))
	for i, s := range skipped {
		out[i] = s.Dict()
	}
	return out
}

func (e *Executors) logSkips(ctx context.Context, pid int64, skipped []mailops.Skip) error {
	if len(skipped) == 0 {
		return nil
	}
	return e.recordFailed(ctx, pid, py.Dict{{Key: "op", Value: "skip"}, {Key: "messages", Value: skipList(skipped)}}, "changed since the proposal")
}

func uidOrNil(copied *mail.CopyUID, uid uint32) any {
	if copied == nil {
		return nil
	}
	if dest, ok := copied.Mapping[uid]; ok {
		return dest
	}
	return nil
}

func validityOrNil(copied *mail.CopyUID) any {
	if copied == nil {
		return nil
	}
	return copied.UIDValidity
}

func sortedUIDs(items []mailops.Verified) ([]uint32, map[uint32]mailops.Verified) {
	byUID := map[uint32]mailops.Verified{}
	for _, item := range items {
		byUID[item.UID] = item
	}
	uids := make([]uint32, 0, len(byUID))
	for uid := range byUID {
		uids = append(uids, uid)
	}
	slices.Sort(uids)
	return uids, byUID
}

func (e *Executors) moveItems(ctx context.Context, client mail.Client, pid int64, account string, items []mailops.Verified, destIMAP string) (int, error) {
	moved := 0
	for _, g := range mailops.GroupByMailbox(items) {
		uids, byUID := sortedUIDs(g.Items)
		action := py.Dict{
			{Key: "op", Value: "move"}, {Key: "account", Value: account}, {Key: "src_imap", Value: g.IMAPName},
			{Key: "src_uidvalidity", Value: g.UIDValidity}, {Key: "dest_imap", Value: destIMAP},
		}
		err := mailops.MoveUIDs(ctx, client, g.IMAPName, destIMAP, uids, func(batch []uint32, copied *mail.CopyUID) error {
			entries := make([]any, len(batch))
			for i, u := range batch {
				entries[i] = py.Dict{
					{Key: "id", Value: byUID[u].ID}, {Key: "msgid", Value: byUID[u].MsgID}, {Key: "uid", Value: u},
					{Key: "dest_uid", Value: uidOrNil(copied, u)},
				}
			}
			undo := py.Dict{
				{Key: "op", Value: live.OpMoveBack}, {Key: "account", Value: account}, {Key: "src_imap", Value: g.IMAPName},
				{Key: "dest_imap", Value: destIMAP}, {Key: "dest_uidvalidity", Value: validityOrNil(copied)}, {Key: "items", Value: entries},
			}
			done := append(slices.Clone(action), py.Pair{Key: "dest_uidvalidity", Value: validityOrNil(copied)}, py.Pair{Key: "items", Value: entries})
			if err := e.record(ctx, pid, done, undo); err != nil {
				return err
			}
			moved += len(batch)
			return nil
		})
		var partial *mailops.PartialMove
		if errors.As(err, &partial) {
			copied := py.Dict{}
			if partial.Copied != nil {
				keys := make([]uint32, 0, len(partial.Copied.Mapping))
				for k := range partial.Copied.Mapping {
					keys = append(keys, k)
				}
				slices.Sort(keys)
				for _, k := range keys {
					copied = append(copied, py.Pair{Key: strconv.FormatUint(uint64(k), 10), Value: partial.Copied.Mapping[k]})
				}
			}
			failed := append(slices.Clone(action), py.Pair{Key: "dest_uidvalidity", Value: validityOrNil(partial.Copied)}, py.Pair{Key: "copied", Value: copied})
			if rerr := e.recordFailed(ctx, pid, failed, partial.Error()); rerr != nil {
				return moved, rerr
			}
		}
		if err != nil {
			return moved, err
		}
	}
	return moved, nil
}

func uidList(uids []uint32) []any {
	out := make([]any, len(uids))
	for i, u := range uids {
		out[i] = u
	}
	return out
}

func stringList(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

func (e *Executors) relabelItems(ctx context.Context, client mail.Client, pid int64, items []mailops.Verified, add, remove []string) (int, error) {
	changed := 0
	for _, r := range mailops.RelabelPlan(items, add, remove) {
		if len(r.Add) == 0 && len(r.Remove) == 0 {
			continue
		}
		for _, g := range mailops.GroupByMailbox(r.Items) {
			uids := make([]uint32, len(g.Items))
			ids := make([]any, len(g.Items))
			for i, item := range g.Items {
				uids[i] = item.UID
				ids[i] = item.ID
			}
			slices.Sort(uids)
			if err := mailops.StoreLabels(ctx, client, g.IMAPName, uids, r.Add, r.Remove); err != nil {
				return changed, err
			}
			base := py.Dict{
				{Key: "op", Value: live.OpGmailLabels}, {Key: "imap_name", Value: g.IMAPName}, {Key: "uidvalidity", Value: g.UIDValidity},
				{Key: "uids", Value: uidList(uids)}, {Key: "ids", Value: ids},
			}
			undo := append(slices.Clone(base), py.Pair{Key: "add", Value: stringList(r.Remove)}, py.Pair{Key: "remove", Value: stringList(r.Add)})
			action := append(slices.Clone(base), py.Pair{Key: "add", Value: stringList(r.Add)}, py.Pair{Key: "remove", Value: stringList(r.Remove)})
			if err := e.record(ctx, pid, action, undo); err != nil {
				return changed, err
			}
			changed += len(g.Items)
		}
	}
	return changed, nil
}

func (e *Executors) trashItems(ctx context.Context, client mail.Client, pid int64, account string, items []mailops.Verified) (int, error) {
	trash, err := mailops.TrashMailbox(ctx, client, account)
	if err != nil {
		return 0, err
	}
	if account != config.Gmail {
		return e.moveItems(ctx, client, pid, account, items, trash)
	}
	trashed := 0
	for _, g := range mailops.GroupByMailbox(items) {
		uids, byUID := sortedUIDs(g.Items)
		if err := client.SelectRW(ctx, g.IMAPName); err != nil {
			return trashed, err
		}
		for _, batch := range mailops.Chunks(uids, mailops.Batch) {
			copied, err := mailops.CopyUIDs(ctx, client, trash, batch)
			if err != nil {
				return trashed, err
			}
			entries := make([]any, len(batch))
			var known []any
			for i, u := range batch {
				dest := uidOrNil(copied, u)
				entries[i] = py.Dict{{Key: "id", Value: byUID[u].ID}, {Key: "uid", Value: u}, {Key: "dest_uid", Value: dest}}
				if dest != nil {
					known = append(known, dest)
				}
			}
			var undo py.Dict
			if len(known) > 0 && len(known) == len(entries) {
				undo = py.Dict{
					{Key: "op", Value: live.OpGmailUntrash}, {Key: "trash_imap", Value: trash},
					{Key: "dest_uidvalidity", Value: validityOrNil(copied)}, {Key: "uids", Value: known},
				}
			}
			action := py.Dict{
				{Key: "op", Value: "trash"}, {Key: "imap_name", Value: g.IMAPName}, {Key: "uidvalidity", Value: g.UIDValidity},
				{Key: "dest_imap", Value: trash}, {Key: "items", Value: entries},
			}
			if err := e.record(ctx, pid, action, undo); err != nil {
				return trashed, err
			}
			trashed += len(batch)
		}
	}
	return trashed, nil
}

func decode(raw json.RawMessage) (py.Dict, error) {
	v, err := py.JSONLoads(raw)
	if err != nil {
		return nil, err
	}
	d, ok := v.(py.Dict)
	if !ok {
		return nil, py.TypeError("'%s' object is not subscriptable", py.TypeName(v))
	}
	return d, nil
}

func required(d py.Dict, key string) (any, error) {
	v, ok := d.Get(key)
	if !ok {
		return nil, fmt.Errorf("%w: %s", httpx.ErrKey, py.Repr(key))
	}
	return v, nil
}

func snapshotsOf(v any) ([]mailops.Snapshot, error) {
	var out []mailops.Snapshot
	err := json.Unmarshal([]byte(py.JSONDumps(v)), &out)
	return out, err
}

func (e *Executors) ExecuteMoves(ctx context.Context, p *proposals.Proposal, selection any) (py.Dict, error) {
	var payload struct {
		Folder string  `json:"folder"`
		Groups []group `json:"groups"`
	}
	if err := json.Unmarshal(p.Payload, &payload); err != nil {
		return nil, err
	}
	plan, err := resolveGroups(payload.Groups, selection)
	if err != nil {
		return nil, err
	}
	account := p.AccountName()
	moved, skipped, dests := 0, []mailops.Skip{}, []any{}
	err = mailops.Session(ctx, e.Open, account, func(client mail.Client) error {
		for _, step := range plan {
			if len(step.snaps) == 0 {
				continue
			}
			items, skips, err := mailops.Verify(ctx, e.DB.Read, client, account, step.snaps)
			if err != nil {
				return err
			}
			skipped = append(skipped, skips...)
			if err := e.logSkips(ctx, p.ID, skips); err != nil {
				return err
			}
			if len(items) == 0 {
				continue
			}
			var n int
			if account == config.Gmail {
				n, err = e.relabelItems(ctx, client, p.ID, items, []string{step.name}, []string{payload.Folder})
			} else {
				dest := mail.UTF7Encode(step.name)
				created, cerr := mailops.EnsureFolder(ctx, client, dest)
				if cerr != nil {
					return cerr
				}
				if created {
					if err := e.record(ctx, p.ID, py.Dict{{Key: "op", Value: "create_folder"}, {Key: "imap_name", Value: dest}},
						py.Dict{{Key: "op", Value: OpDeleteFolder}, {Key: "account", Value: account}, {Key: "imap_name", Value: dest}}); err != nil {
						return err
					}
				}
				n, err = e.moveItems(ctx, client, p.ID, account, items, dest)
			}
			moved += n
			if err != nil {
				return err
			}
			dests = append(dests, step.name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return py.Dict{
		{Key: "summary", Value: fmt.Sprintf("%d moved, %d skipped as changed", moved, len(skipped))},
		{Key: "moved", Value: moved}, {Key: "skipped", Value: skipList(skipped)}, {Key: "destinations", Value: dests},
	}, nil
}

func (e *Executors) ExecuteDeletion(ctx context.Context, p *proposals.Proposal, selection any) (py.Dict, error) {
	payload, err := decode(p.Payload)
	if err != nil {
		return nil, err
	}
	messages, err := required(payload, "messages")
	if err != nil {
		return nil, err
	}
	all, err := snapshotsOf(messages)
	if err != nil {
		return nil, err
	}
	ids, err := selected(selection, "message_ids")
	if err != nil {
		return nil, err
	}
	snaps, err := chosenSnapshots(all, ids)
	if err != nil {
		return nil, err
	}
	rule, err := required(payload, "rule")
	if err != nil {
		return nil, err
	}
	ruleDict, ok := rule.(py.Dict)
	if !ok {
		return nil, py.TypeError("'%s' object is not subscriptable", py.TypeName(rule))
	}
	id, err := required(ruleDict, "id")
	if err != nil {
		return nil, err
	}
	var one int
	switch err := e.DB.Write.QueryRowContext(ctx, "SELECT 1 FROM rules WHERE id = ?", id).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		one = 0
	case err != nil:
		return nil, err
	}
	created := false
	if one == 0 {
		if _, err := ruleedit.CreateRule(ctx, e.DB, ruleDict); err != nil {
			return nil, err
		}
		created = true
		if err := e.record(ctx, p.ID, py.Dict{{Key: "op", Value: "rule_create"}, {Key: "id", Value: id}},
			py.Dict{{Key: "op", Value: OpRuleDelete}, {Key: "id", Value: id}}); err != nil {
			return nil, err
		}
	}
	trashed, skipped := 0, []mailops.Skip{}
	if len(snaps) > 0 {
		account := p.AccountName()
		err := mailops.Session(ctx, e.Open, account, func(client mail.Client) error {
			items, skips, err := mailops.Verify(ctx, e.DB.Read, client, account, snaps)
			if err != nil {
				return err
			}
			skipped = append(skipped, skips...)
			if err := e.logSkips(ctx, p.ID, skips); err != nil {
				return err
			}
			if len(items) > 0 {
				trashed, err = e.trashItems(ctx, client, p.ID, account, items)
			}
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	state := "already present"
	if created {
		state = "created"
	}
	return py.Dict{
		{Key: "summary", Value: fmt.Sprintf("rule %s, %d moved to Trash, %d skipped as changed", state, trashed, len(skipped))},
		{Key: "rule", Value: id}, {Key: "trashed", Value: trashed}, {Key: "skipped", Value: skipList(skipped)},
	}, nil
}

func (e *Executors) ExecuteLabelMerge(ctx context.Context, p *proposals.Proposal, selection any) (py.Dict, error) {
	var payload struct {
		Label    string             `json:"label"`
		Into     string             `json:"into"`
		Messages []mailops.Snapshot `json:"messages"`
	}
	if err := json.Unmarshal(p.Payload, &payload); err != nil {
		return nil, err
	}
	ids, err := selected(selection, "message_ids")
	if err != nil {
		return nil, err
	}
	snaps, err := chosenSnapshots(payload.Messages, ids)
	if err != nil {
		return nil, err
	}
	account := p.AccountName()
	var moved int
	var skipped []mailops.Skip
	deleted := false
	err = mailops.Session(ctx, e.Open, account, func(client mail.Client) error {
		items, skips, err := mailops.Verify(ctx, e.DB.Read, client, account, snaps)
		if err != nil {
			return err
		}
		skipped = skips
		if err := e.logSkips(ctx, p.ID, skips); err != nil {
			return err
		}
		if moved, err = e.relabelItems(ctx, client, p.ID, items, []string{payload.Into}, []string{payload.Label}); err != nil {
			return err
		}
		label := mail.UTF7Encode(payload.Label)
		st, err := client.Status(ctx, label)
		if err != nil || st.Messages != 0 {
			return err
		}
		if deleted, err = mailops.DeleteFolderIfEmpty(ctx, client, label); err != nil || !deleted {
			return err
		}
		return e.record(ctx, p.ID, py.Dict{{Key: "op", Value: "delete_label"}, {Key: "imap_name", Value: label}}, nil)
	})
	if err != nil {
		return nil, err
	}
	state := "kept"
	if deleted {
		state = "deleted"
	}
	return py.Dict{
		{Key: "summary", Value: fmt.Sprintf("%d relabelled, label %s, %d skipped as changed", moved, state, len(skipped))},
		{Key: "relabelled", Value: moved}, {Key: "label_deleted", Value: deleted}, {Key: "skipped", Value: skipList(skipped)},
	}, nil
}

type DeleteFolder struct {
	Account  string `json:"account"`
	IMAPName string `json:"imap_name"`
}

type RuleDelete struct {
	ID string `json:"id"`
}

func (e *Executors) UndoDeleteFolder(ctx context.Context, raw json.RawMessage) (py.Dict, error) {
	var plan DeleteFolder
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, err
	}
	var deleted bool
	err := mailops.Session(ctx, e.Open, plan.Account, func(client mail.Client) error {
		var err error
		deleted, err = mailops.DeleteFolderIfEmpty(ctx, client, plan.IMAPName)
		return err
	})
	if err != nil {
		return nil, err
	}
	return py.Dict{{Key: "deleted", Value: deleted}}, nil
}

func (e *Executors) UndoRuleDelete(ctx context.Context, raw json.RawMessage) (py.Dict, error) {
	var plan RuleDelete
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, err
	}
	deleted, err := ruleedit.DeleteRule(ctx, e.DB, plan.ID)
	if err != nil {
		return nil, err
	}
	return py.Dict{{Key: "deleted", Value: deleted}}, nil
}

func SyncAfter(syncer *mailsync.Syncer, open mailops.Opener) proposals.SyncHook {
	return func(ctx context.Context, account string) error {
		lock, err := db.Exclusive(syncer.Cfg.DBPath)
		if err != nil {
			return err
		}
		defer func() { _ = lock.Close() }()
		_, err = syncer.One(ctx, account, mailsync.Opener(open))
		return err
	}
}

func Register(r *proposals.Registry, e *Executors, syncer *mailsync.Syncer) {
	r.Executors[RebalanceSplit] = e.ExecuteMoves
	r.Executors[RebalanceMerge] = e.ExecuteMoves
	r.Executors[DeletionRule] = e.ExecuteDeletion
	r.Executors[GmailLabelMerge] = e.ExecuteLabelMerge
	r.Executors[InboxBacklog] = e.ExecuteMoves
	live.RegisterUndoers(r, e.Open)
	r.Undoers[OpDeleteFolder] = e.UndoDeleteFolder
	r.Undoers[OpRuleDelete] = e.UndoRuleDelete
	r.Sync = SyncAfter(syncer, e.Open)
}
