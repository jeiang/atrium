package executors

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/ruleedit"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/proposals"
)

var log = logx.Logger("atrium.mail.executors")

const (
	RebalanceSplit  = "rebalance_split"
	RebalanceMerge  = "rebalance_merge"
	DeletionRule    = "deletion_rule"
	GmailLabelMerge = "gmail_label_merge"
	InboxBacklog    = "inbox_backlog"

	OpDeleteFolder = "delete_folder"
	OpRuleDelete   = "rule_delete"
	OpCancelSnooze = "cancel_snooze"
)

func chosenSnapshots(messages []mailops.Snapshot, wanted any) ([]mailops.Snapshot, error) {
	if wanted == nil {
		return slices.Clone(messages), nil
	}
	items, ok := wanted.([]any)
	if !ok {
		return nil, proposals.SelectionError("selection is not a list")
	}
	ids := map[int64]bool{}
	outside := false
	for _, item := range items {
		id, ok := numberToInt64(item)
		if !ok {
			outside = true
			continue
		}
		ids[id] = true
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

type movesResult struct {
	Summary      string `json:"summary"`
	Moved        int    `json:"moved"`
	Skipped      []any  `json:"skipped"`
	Destinations []any  `json:"destinations"`
}

type deletionResult struct {
	Summary string `json:"summary"`
	Rule    string `json:"rule"`
	Trashed int    `json:"trashed"`
	Skipped []any  `json:"skipped"`
}

type labelMergeResult struct {
	Summary         string `json:"summary"`
	Relabelled      int    `json:"relabelled"`
	LabelDeleted    bool   `json:"label_deleted"`
	LabelKeptReason any    `json:"label_kept_reason"`
	Skipped         []any  `json:"skipped"`
}

func withPairs(base map[string]any, kv ...any) map[string]any {
	out := maps.Clone(base)
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func get(d map[string]any, key string) any {
	return d[key]
}

func selected(selection any, key string) (any, error) {
	if !truthy(selection) {
		return nil, nil
	}
	d, ok := selection.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("selection is not an object")
	}
	return get(d, key), nil
}

func resolveGroups(groups []group, selection any) ([]planned, error) {
	picksV, err := selected(selection, "groups")
	if err != nil {
		return nil, err
	}
	byID := map[string]map[string]any{}
	if picksV != nil {
		list, ok := picksV.([]any)
		if !ok {
			return nil, proposals.SelectionError("selection does not match the proposal groups")
		}
		if len(list) != len(groups) {
			return nil, proposals.SelectionError("selection does not match the proposal groups")
		}
		for _, value := range list {
			d, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("selection group is not an object")
			}
			id := d["id"]
			name, ok := id.(string)
			if !ok || name == "" {
				return nil, proposals.SelectionError("selection group is missing its id")
			}
			if _, duplicate := byID[name]; duplicate {
				return nil, proposals.SelectionError("selection repeats group " + strconv.Quote(name))
			}
			byID[name] = d
		}
	}
	plan := make([]planned, 0, len(groups))
	for _, g := range groups {
		pick := map[string]any{}
		if picksV != nil {
			found, ok := byID[g.Dest]
			if !ok {
				return nil, proposals.SelectionError("selection does not name group " + strconv.Quote(g.Dest))
			}
			pick = found
		}
		value := g.Dest
		if chosen := get(pick, "name"); truthy(chosen) {
			s, ok := chosen.(string)
			if !ok {
				return nil, fmt.Errorf("selection name is not text")
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
	if picksV != nil && len(byID) != len(groups) {
		return nil, proposals.SelectionError("selection names a group the proposal does not have")
	}
	return plan, nil
}

type Executors struct {
	DB     *db.DB
	Ledger *ledger.Store
	Open   mail.OpenFunc
	Now    func() float64
}

func (e *Executors) now() float64 {
	if e.Now != nil {
		return e.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (e *Executors) store() *ledger.Store {
	if e.Ledger == nil {
		panic("executors: the effect ledger store is not configured")
	}
	return e.Ledger
}

func jsonOf(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return nil
	}
	return b
}

func (e *Executors) effect(pid int64, action map[string]any) ledger.New {
	op, _ := action["op"].(string)
	account, _ := action["account"].(string)
	src, _ := action["src_imap"].(string)
	dest, _ := action["dest_imap"].(string)
	return ledger.New{
		Origin: "proposal", Operation: op, OperationKey: operationKey(pid, action),
		Target: fmt.Sprintf("proposal:%d", pid), ProposalID: &pid,
		Account: account, Src: src, Dest: dest, Action: jsonOf(action),
	}
}

func operationKey(pid int64, action map[string]any) string {
	op, _ := action["op"].(string)
	key := fmt.Sprintf("proposal:%d:%s", pid, op)
	for _, field := range []string{"imap_name", "src_imap"} {
		if value, ok := action[field].(string); ok && value != "" {
			key += ":" + value
		}
	}
	if uids, ok := action["uids"].([]any); ok && len(uids) > 0 {
		parts := make([]string, len(uids))
		for i, uid := range uids {
			parts[i] = fmt.Sprint(uid)
		}
		key += ":" + strings.Join(parts, ",")
	}
	for _, field := range []string{"id", "snooze_id"} {
		if value, ok := action[field]; ok {
			key += ":" + fmt.Sprint(value)
		}
	}
	return key
}

func (e *Executors) record(ctx context.Context, pid int64, action, undo map[string]any) error {
	_, err := e.store().ApplyLocal(ctx, e.effect(pid, action), func(context.Context, *sql.Tx) (ledger.Result, error) {
		return ledger.Result{State: ledger.Done, After: jsonOf(action), Undo: jsonOf(undo)}, nil
	})
	return err
}

func (e *Executors) recordFailed(ctx context.Context, pid int64, action map[string]any, text string) error {
	_, err := e.store().ApplyLocal(ctx, e.effect(pid, action), func(context.Context, *sql.Tx) (ledger.Result, error) {
		return ledger.Result{State: ledger.Failed, After: jsonOf(action), Error: text}, nil
	})
	return err
}

type slot struct {
	e      *Executors
	pid    int64
	ticket ledger.Ticket
	uids   []uint32
	stop   func()
}

func (e *Executors) hold(ctx context.Context, pid int64, action map[string]any, uids []uint32, resources []ledger.Resource) (*slot, error) {
	logged := withPairs(action, "uids", uidList(uids))
	n := e.effect(pid, logged)
	n.Resources = resources
	ticket, err := e.store().Begin(ctx, n, nil)
	if err != nil {
		return nil, err
	}
	return &slot{e: e, pid: pid, ticket: ticket, uids: uids, stop: keepAlive(e.store(), ticket)}, nil
}

func keepAlive(s *ledger.Store, t ledger.Ticket) func() {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(ledger.RenewSeconds * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := s.Heartbeat(context.Background(), t); err != nil {
					log.Warn("in-flight effect lease not renewed", logx.Err, err.Error())
				}
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

func (s *slot) settle(ctx context.Context, action, undo map[string]any, state ledger.State, text *string) error {
	if s.ticket.ID == 0 {
		return nil
	}
	ticket := s.ticket
	s.ticket = ledger.Ticket{}
	s.stop()
	result := ledger.Result{State: state, After: jsonOf(action), Undo: jsonOf(undo)}
	if text != nil {
		result.Error = *text
	}
	return s.e.store().Settle(ctx, ticket, result, nil)
}

func (s *slot) record(ctx context.Context, action, undo map[string]any) error {
	return s.settle(ctx, action, undo, ledger.Done, nil)
}

func (s *slot) partial(ctx context.Context, action, undo map[string]any, text string) error {
	return s.settle(ctx, action, undo, ledger.Partial, &text)
}

func (s *slot) release(ctx context.Context) error {
	return s.settle(ctx, nil, nil, ledger.Skipped, new("the write did not start"))
}

func (s *slot) fail(ctx context.Context, action map[string]any, err error) error {
	if err == nil {
		return s.release(ctx)
	}
	state := ledger.Failed
	text := err.Error()
	if mailops.OutcomeUnknown(err) {
		state = ledger.Unknown
		if partial, ok := errors.AsType[*mailops.PartialMove](err); ok {
			text = partial.Err.Error()
		}
	}
	logged := withPairs(action, "uids", uidList(s.uids))
	if rerr := s.settle(ctx, logged, nil, state, &text); rerr != nil {
		return rerr
	}
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
	return e.recordFailed(ctx, pid, map[string]any{"op": "skip", "messages": skipList(skipped)}, "changed since the proposal")
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
		n, err := e.moveGroup(ctx, client, pid, account, g, destIMAP)
		moved += n
		if err != nil {
			return moved, err
		}
	}
	return moved, nil
}

func (e *Executors) moveGroup(ctx context.Context, client mail.Client, pid int64, account string, g mailops.Group, destIMAP string) (moved int, err error) {
	uids, byUID := sortedUIDs(g.Items)
	action := map[string]any{"op": "move", "account": account, "src_imap": g.IMAPName, "src_uidvalidity": g.UIDValidity, "dest_imap": destIMAP}
	size, err := mailops.BatchSize(ctx, client)
	if err != nil {
		return 0, err
	}
	chunks := mailops.Chunks(uids, size)
	resources := func(batch []uint32) []ledger.Resource {
		out := make([]ledger.Resource, len(batch))
		for i, uid := range batch {
			out[i] = ledger.Resource{Key: ledger.MailResource(account, g.IMAPName, g.UIDValidity, uid)}
		}
		return out
	}
	next := 0
	s, err := e.hold(ctx, pid, action, chunks[next], resources(chunks[next]))
	if err != nil {
		return 0, err
	}
	defer func() {
		if rerr := s.release(ctx); rerr != nil && err == nil {
			err = rerr
		}
	}()
	err = mailops.MoveUIDs(ctx, client, g.IMAPName, destIMAP, g.UIDValidity, uids, size, func(batch []uint32, copied *mail.CopyUID) error {
		entries := make([]any, len(batch))
		for i, u := range batch {
			entries[i] = map[string]any{"id": byUID[u].ID, "msgid": byUID[u].MsgID, "uid": u, "dest_uid": uidOrNil(copied, u)}
		}
		undo := map[string]any{"op": live.OpMoveBack, "account": account, "src_imap": g.IMAPName, "dest_imap": destIMAP, "dest_uidvalidity": validityOrNil(copied), "items": entries}
		done := withPairs(action, "dest_uidvalidity", validityOrNil(copied), "items", entries)
		if err := s.record(ctx, done, undo); err != nil {
			return err
		}
		moved += len(batch)
		if next++; next < len(chunks) {
			held, err := e.hold(ctx, pid, action, chunks[next], resources(chunks[next]))
			if err != nil {
				return err
			}
			s = held
		}
		return nil
	})
	if partial, ok := errors.AsType[*mailops.PartialMove](err); ok && !partial.Unconfirmed {
		copied := map[string]any{}
		if partial.Copied != nil {
			keys := make([]uint32, 0, len(partial.Copied.Mapping))
			for k := range partial.Copied.Mapping {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				copied[strconv.FormatUint(uint64(k), 10)] = partial.Copied.Mapping[k]
			}
		}
		failed := withPairs(action, "dest_uidvalidity", validityOrNil(partial.Copied), "copied", copied)
		if rerr := s.partial(ctx, failed, nil, partial.Error()); rerr != nil {
			return moved, rerr
		}
		return moved, err
	}
	if err != nil {
		return moved, s.fail(ctx, action, err)
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

func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func stringList(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

func (e *Executors) relabelItems(ctx context.Context, client mail.Client, pid int64, account string, items []mailops.Verified, add, remove []string) (int, error) {
	changed := 0
	size, err := mailops.BatchSize(ctx, client)
	if err != nil {
		return 0, err
	}
	for _, r := range mailops.RelabelPlan(items, add, remove) {
		if len(r.Add) == 0 && len(r.Remove) == 0 {
			continue
		}
		for _, g := range mailops.GroupByMailbox(r.Items) {
			for _, chunk := range mailops.Chunks(sortedItems(g.Items), size) {
				n, err := e.relabelChunk(ctx, client, pid, account, g, chunk, size, r)
				changed += n
				if err != nil {
					return changed, err
				}
			}
		}
	}
	return changed, nil
}

func sortedItems(items []mailops.Verified) []mailops.Verified {
	sorted := slices.Clone(items)
	slices.SortFunc(sorted, func(a, b mailops.Verified) int { return cmp.Compare(a.UID, b.UID) })
	return sorted
}

func (e *Executors) relabelChunk(ctx context.Context, client mail.Client, pid int64, account string, g mailops.Group, chunk []mailops.Verified, size int, r mailops.Relabel) (int, error) {
	uids := make([]uint32, len(chunk))
	ids := make([]any, len(chunk))
	for i, item := range chunk {
		uids[i] = item.UID
		ids[i] = item.ID
	}
	attempt := map[string]any{"op": live.OpGmailLabels, "imap_name": g.IMAPName, "uidvalidity": g.UIDValidity, "add": stringList(r.Add), "remove": stringList(r.Remove)}
	resources := make([]ledger.Resource, len(uids))
	for i, uid := range uids {
		resources[i] = ledger.Resource{Key: ledger.MailResource(account, g.IMAPName, g.UIDValidity, uid)}
	}
	s, err := e.hold(ctx, pid, attempt, uids, resources)
	if err != nil {
		return 0, err
	}
	if err := mailops.StoreLabels(ctx, client, g.IMAPName, g.UIDValidity, uids, size, r.Add, r.Remove); err != nil {
		return 0, s.fail(ctx, attempt, err)
	}
	base := map[string]any{"op": live.OpGmailLabels, "account": account, "imap_name": g.IMAPName, "uidvalidity": g.UIDValidity, "uids": uidList(uids), "ids": ids}
	undo := withPairs(base, "add", stringList(r.Remove), "remove", stringList(r.Add))
	action := withPairs(base, "add", stringList(r.Add), "remove", stringList(r.Remove))
	if err := s.record(ctx, action, undo); err != nil {
		return 0, err
	}
	return len(chunk), nil
}

func (e *Executors) trashItems(ctx context.Context, client mail.Client, pid int64, account string, items []mailops.Verified) (int, error) {
	trash, err := mailops.TrashMailbox(ctx, client, account)
	if err != nil {
		return 0, err
	}
	if config.From(ctx).Preset(account) != config.Gmail {
		return e.moveItems(ctx, client, pid, account, items, trash)
	}
	trashed := 0
	size, err := mailops.BatchSize(ctx, client)
	if err != nil {
		return 0, err
	}
	for _, g := range mailops.GroupByMailbox(items) {
		uids, byUID := sortedUIDs(g.Items)
		if err := mailops.SelectRW(ctx, client, g.IMAPName, g.UIDValidity); err != nil {
			return trashed, err
		}
		for _, batch := range mailops.Chunks(uids, size) {
			attempt := map[string]any{"op": "trash", "imap_name": g.IMAPName, "uidvalidity": g.UIDValidity, "dest_imap": trash}
			resources := make([]ledger.Resource, len(batch))
			for i, uid := range batch {
				resources[i] = ledger.Resource{Key: ledger.MailResource(account, g.IMAPName, g.UIDValidity, uid)}
			}
			s, err := e.hold(ctx, pid, attempt, batch, resources)
			if err != nil {
				return trashed, err
			}
			copied, err := mailops.CopyUIDs(ctx, client, trash, batch)
			if err != nil {
				return trashed, s.fail(ctx, attempt, err)
			}
			entries := make([]any, len(batch))
			var known []any
			for i, u := range batch {
				dest := uidOrNil(copied, u)
				entries[i] = map[string]any{"id": byUID[u].ID, "uid": u, "dest_uid": dest}
				if dest != nil {
					known = append(known, dest)
				}
			}
			var undo map[string]any
			if len(known) > 0 {
				undo = map[string]any{"op": live.OpGmailUntrash, "account": account, "trash_imap": trash, "dest_uidvalidity": validityOrNil(copied), "uids": known}
			}
			action := map[string]any{"op": "trash", "imap_name": g.IMAPName, "uidvalidity": g.UIDValidity, "dest_imap": trash, "items": entries}
			if len(known) < len(entries) {
				text := fmt.Sprintf("partial: %d of %d messages have no destination uid in Trash; check the mailbox", len(entries)-len(known), len(entries))
				if err := s.partial(ctx, action, undo, text); err != nil {
					return trashed, err
				}
				trashed += len(batch)
				continue
			}
			if err := s.record(ctx, action, undo); err != nil {
				return trashed, err
			}
			trashed += len(batch)
		}
	}
	return trashed, nil
}

func decode(raw json.RawMessage) (map[string]any, error) {
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	if d == nil {
		return nil, fmt.Errorf("proposal payload is not an object")
	}
	return d, nil
}

func required(d map[string]any, key string) (any, error) {
	v, ok := d[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", httpx.ErrMissing, strconv.Quote(key))
	}
	return v, nil
}

func snapshotsOf(v any) ([]mailops.Snapshot, error) {
	var out []mailops.Snapshot
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}

func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

func numberToInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), n == float64(int64(n))
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}

func (e *Executors) ExecuteMoves(ctx context.Context, p *proposals.Proposal, selection any) (any, error) {
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
	err = mailops.Session(ctx, e.Open, e.DB.Read, account, func(client mail.Client) error {
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
			if config.From(ctx).Preset(account) == config.Gmail {
				n, err = e.relabelItems(ctx, client, p.ID, account, items, []string{step.name}, []string{payload.Folder})
			} else {
				dest := mail.UTF7Encode(step.name)
				create := map[string]any{"op": "create_folder", "account": account, "imap_name": dest}
				s, herr := e.hold(ctx, p.ID, create, nil, []ledger.Resource{{Key: "mail_folder:" + account + ":" + dest}})
				if herr != nil {
					return herr
				}
				created, cerr := mailops.EnsureFolder(ctx, client, dest)
				if cerr != nil {
					return s.fail(ctx, create, cerr)
				}
				if created {
					if err := s.record(ctx, create,
						map[string]any{"op": OpDeleteFolder, "account": account, "imap_name": dest}); err != nil {
						return err
					}
				}
				if err := s.release(ctx); err != nil {
					return err
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
	return movesResult{Summary: fmt.Sprintf("%d moved, %d skipped as changed", moved, len(skipped)), Moved: moved, Skipped: skipList(skipped), Destinations: dests}, nil
}

func (e *Executors) ExecuteDeletion(ctx context.Context, p *proposals.Proposal, selection any) (any, error) {
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
	ruleDict, ok := rule.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("proposal rule is not an object")
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
		if _, err := e.store().ApplyLocal(ctx, e.effect(p.ID, map[string]any{"op": "rule_create", "id": id}),
			func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
				if _, err := ruleedit.CreateRuleTx(ctx, tx, ruleDict); err != nil {
					return ledger.Result{}, err
				}
				return ledger.Result{State: ledger.Done, After: jsonOf(map[string]any{"op": "rule_create", "id": id}),
					Undo: jsonOf(map[string]any{"op": OpRuleDelete, "id": id})}, nil
			}); err != nil {
			return nil, err
		}
		created = true
	}
	trashed, skipped := 0, []mailops.Skip{}
	if len(snaps) > 0 {
		account := p.AccountName()
		err := mailops.Session(ctx, e.Open, e.DB.Read, account, func(client mail.Client) error {
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
	return deletionResult{Summary: fmt.Sprintf("rule %s, %d moved to Trash, %d skipped as changed", state, trashed, len(skipped)), Rule: fmt.Sprint(id), Trashed: trashed, Skipped: skipList(skipped)}, nil
}

func (e *Executors) ExecuteLabelMerge(ctx context.Context, p *proposals.Proposal, selection any) (any, error) {
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
	keptReason := ""
	err = mailops.Session(ctx, e.Open, e.DB.Read, account, func(client mail.Client) error {
		items, skips, err := mailops.Verify(ctx, e.DB.Read, client, account, snaps)
		if err != nil {
			return err
		}
		skipped = skips
		if err := e.logSkips(ctx, p.ID, skips); err != nil {
			return err
		}
		if moved, err = e.relabelItems(ctx, client, p.ID, account, items, []string{payload.Into}, []string{payload.Label}); err != nil {
			return err
		}
		label := mail.UTF7Encode(payload.Label)
		st, err := client.Status(ctx, label)
		if err != nil {
			return err
		}
		if st.Messages != 0 {
			keptReason = fmt.Sprintf("%d messages still carry the label", st.Messages)
			return nil
		}
		drop := map[string]any{"op": "delete_label", "account": account, "imap_name": label}
		s, err := e.hold(ctx, p.ID, drop, nil, []ledger.Resource{{Key: "mail_folder:" + account + ":" + label}})
		if err != nil {
			return err
		}
		if deleted, err = mailops.DeleteFolderIfEmpty(ctx, client, label); err != nil || !deleted {
			return s.fail(ctx, drop, err)
		}
		return s.record(ctx, drop, nil)
	})
	if err != nil {
		return nil, err
	}
	state := "kept"
	if deleted {
		state = "deleted"
	}
	summary := fmt.Sprintf("%d relabelled, label %s", moved, state)
	if keptReason != "" {
		summary += " (" + keptReason + ")"
	}
	summary += fmt.Sprintf(", %d skipped as changed", len(skipped))
	return labelMergeResult{Summary: summary, Relabelled: moved, LabelDeleted: deleted, LabelKeptReason: nullableString(keptReason), Skipped: skipList(skipped)}, nil
}

type DeleteFolder struct {
	Account  string `json:"account"`
	IMAPName string `json:"imap_name"`
}

type RuleDelete struct {
	ID string `json:"id"`
}

func (e *Executors) UndoDeleteFolder(ctx context.Context, raw json.RawMessage) (any, error) {
	var plan DeleteFolder
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, err
	}
	var deleted bool
	err := mailops.Session(ctx, e.Open, e.DB.Read, plan.Account, func(client mail.Client) error {
		var err error
		deleted, err = mailops.DeleteFolderIfEmpty(ctx, client, plan.IMAPName)
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"deleted": deleted}, nil
}

func (e *Executors) UndoRuleDelete(ctx context.Context, tx *sql.Tx, raw json.RawMessage) (ledger.Result, error) {
	var plan RuleDelete
	if err := json.Unmarshal(raw, &plan); err != nil {
		return ledger.Result{}, err
	}
	deleted, err := ruleedit.DeleteRuleTx(ctx, tx, plan.ID)
	if err != nil {
		return ledger.Result{}, err
	}
	return ledger.Result{State: ledger.Done, After: jsonOf(map[string]any{"deleted": deleted})}, nil
}

func validatePlan(name string) func(json.RawMessage) error {
	return func(raw json.RawMessage) error {
		var head struct {
			Op string `json:"op"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return ledger.Refused("proposal undo payload is invalid")
		}
		if head.Op != name {
			return ledger.Refused("proposal undo payload names " + head.Op)
		}
		return nil
	}
}

func (e *Executors) Undo() []ledger.UndoDefinition {
	external := []struct {
		name string
		fn   func(context.Context, json.RawMessage) (any, error)
	}{
		{OpDeleteFolder, e.UndoDeleteFolder},
	}
	var out []ledger.UndoDefinition
	for _, u := range external {
		u := u
		out = append(out, ledger.UndoDefinition{Operation: u.name, Undoer: ledger.Undoer{
			Validate: validatePlan(u.name),
			External: func(ctx context.Context, raw json.RawMessage) (ledger.Result, error) {
				value, err := u.fn(ctx, raw)
				if err != nil {
					state := ledger.Failed
					if mailops.OutcomeUnknown(err) {
						state = ledger.Unknown
					}
					return ledger.Result{State: state, Error: err.Error()}, nil
				}
				return ledger.Result{State: ledger.Done, After: jsonOf(value)}, nil
			},
		}})
	}
	return append(out,
		ledger.UndoDefinition{Operation: OpRuleDelete, Undoer: ledger.Undoer{Validate: validatePlan(OpRuleDelete), Local: e.UndoRuleDelete}},
		ledger.UndoDefinition{Operation: OpCancelSnooze, Undoer: ledger.Undoer{Validate: validatePlan(OpCancelSnooze), Local: e.UndoCancelSnooze}},
	)
}

func SyncAfter(syncer *mailsync.Syncer, open mail.OpenFunc) proposals.SyncHook {
	return func(ctx context.Context, account string) error {
		lock, err := db.Exclusive(ctx, syncer.Cfg.DBPath, db.ShortLockWait)
		if err != nil {
			return err
		}
		defer func() { _ = lock.Close() }()
		_, err = syncer.One(ctx, account, open)
		return err
	}
}

func (e *Executors) Proposals(owner string) []proposals.Definition {
	return []proposals.Definition{
		{Kind: proposals.Kind{Name: RebalanceSplit, Module: owner, Label: "Split folder", Icon: "split", Picker: true, Groups: true}, Execute: e.ExecuteMoves},
		{Kind: proposals.Kind{Name: RebalanceMerge, Module: owner, Label: "Merge folders", Icon: "merge", Picker: true, Group: true}, Execute: e.ExecuteMoves},
		{Kind: proposals.Kind{Name: DeletionRule, Module: owner, Label: "Deletion rule", Icon: "trash-2", Picker: true, Messages: true, CountKey: "backlog"}, Execute: e.ExecuteDeletion},
		{Kind: proposals.Kind{Name: GmailLabelMerge, Module: owner, Label: "Merge labels", Icon: "tag", Picker: true, Messages: true, CountKey: "from_count"}, Execute: e.ExecuteLabelMerge},
		{Kind: proposals.Kind{Name: InboxBacklog, Module: owner, Label: "Inbox backlog", Icon: "inbox", Picker: true, Groups: true}, Execute: e.ExecuteMoves},
		{Kind: proposals.Kind{Name: MailTriage, Module: owner, Label: "Mail triage", Icon: "inbox", Picker: true, Messages: true, CountKey: "count"}, Execute: e.ExecuteTriage},
	}
}
