package mailops

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/features"
	"github.com/jeiang/atrium/internal/mail/mime"
	"github.com/jeiang/atrium/internal/py"
)

const (
	Batch      = 100
	Flagged    = `\Flagged`
	Seen       = `\Seen`
	Deleted    = `\Deleted`
	InboxLabel = `\Inbox`
	TrashLabel = `\Trash`
)

const (
	ReasonGone        = "gone"
	ReasonMoved       = "moved"
	ReasonChanged     = "changed"
	ReasonUIDValidity = "uidvalidity"
	ReasonMissing     = "missing"
	ReasonMsgID       = "msgid"
	ReasonUnresolved  = "unresolved"
)

func BlockingActionSQL(alias string) string {
	return alias + ".ok = 0 AND json_extract(" + alias + ".action, '$.op') IS NOT 'undo' AND (instr(" + alias + ".error, '" +
		UnknownPrefix + "') > 0 OR instr(" + alias + ".error, '" + PartialPrefix + "') > 0)"
}

var UnresolvedSQL = "(EXISTS (SELECT 1 FROM proposal_actions pa, json_each(pa.action, '$.uids') u WHERE " + BlockingActionSQL("pa") + " AND " +
	"u.value = m.uid AND COALESCE(json_extract(pa.action, '$.src_uidvalidity'), json_extract(pa.action, '$.uidvalidity')) = m.uidvalidity " +
	"AND COALESCE(json_extract(pa.action, '$.src_imap'), json_extract(pa.action, '$.imap_name')) = mb.imap_name) OR " +
	"EXISTS (SELECT 1 FROM proposal_actions pa, json_each(pa.action, '$.copied') u WHERE " + BlockingActionSQL("pa") + " AND " +
	"u.key = CAST(m.uid AS TEXT) AND json_extract(pa.action, '$.src_uidvalidity') = m.uidvalidity " +
	"AND json_extract(pa.action, '$.src_imap') = mb.imap_name))"

var trashNames = map[string]bool{"deleted messages": true, "deleted items": true, "trash": true, "bin": true}

type Error string

func (e Error) Error() string { return string(e) }

func (Error) PyClass() string { return "MailOpError" }

func (Error) PyModule() string { return "atrium.mailops" }

var ErrValidityChanged = Error("UIDVALIDITY changed")

type PartialMove struct {
	Copied      *mail.CopyUID
	Err         error
	Unconfirmed bool
}

const UnknownPrefix = "cancelled, write outcome unknown"

func Blocks(text string) bool {
	return strings.Contains(text, PartialPrefix) || strings.Contains(text, UnknownPrefix)
}

func OutcomeUnknown(err error) bool {
	if partial, ok := errors.AsType[*PartialMove](err); ok {
		return partial.Unconfirmed
	}
	_, ok := errors.AsType[*mail.WriteInterrupted](err)
	return ok
}

func UnknownText(err error, text string) string {
	if OutcomeUnknown(err) && !Blocks(text) {
		return UnknownPrefix + ": " + text
	}
	return text
}

const PartialPrefix = "copied but not expunged: "

func (e *PartialMove) Error() string {
	if e.Unconfirmed {
		return UnknownPrefix + ": " + e.Err.Error()
	}
	return PartialPrefix + e.Err.Error()
}

func (*PartialMove) PyClass() string { return "PartialMove" }

func (*PartialMove) PyModule() string { return "atrium.mailops" }

func (e *PartialMove) Unwrap() error { return e.Err }

type Opener func(ctx context.Context, account string) (mail.Client, error)

type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func Session(ctx context.Context, open Opener, account string, fn func(mail.Client) error) error {
	client, err := open(ctx, account)
	if err != nil {
		return err
	}
	defer client.Close()
	return fn(client)
}

func Chunks[T any](items []T, size int) [][]T {
	var out [][]T
	for start := 0; start < len(items); start += size {
		out = append(out, items[start:min(start+size, len(items))])
	}
	return out
}

type Snapshot struct {
	ID          int64  `json:"id"`
	UID         uint32 `json:"uid"`
	UIDValidity uint32 `json:"uidvalidity"`
	MsgID       string `json:"msgid"`
	Digest      string `json:"digest"`
	Mailbox     string `json:"mailbox"`
	IMAPName    string `json:"imap_name"`
}

func (s Snapshot) Dict() py.Dict {
	return py.Dict{
		{Key: "id", Value: s.ID}, {Key: "uid", Value: s.UID}, {Key: "uidvalidity", Value: s.UIDValidity},
		{Key: "msgid", Value: s.MsgID}, {Key: "digest", Value: s.Digest}, {Key: "mailbox", Value: s.Mailbox},
		{Key: "imap_name", Value: s.IMAPName},
	}
}

type Verified struct {
	Snapshot
	Flags  []string
	Labels []string
}

type Skip struct {
	ID     int64
	Reason string
}

func (s Skip) Dict() py.Dict {
	return py.Dict{{Key: "id", Value: s.ID}, {Key: "reason", Value: s.Reason}}
}

func Snapshots(ctx context.Context, q Querier, ids []int64) ([]Snapshot, error) {
	var out []Snapshot
	for _, chunk := range Chunks(ids, 500) {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := q.QueryContext(ctx, "SELECT m.id, m.uid, m.uidvalidity, m.msgid, m.digest, mb.name, mb.imap_name "+
			"FROM messages m JOIN mailboxes mb ON mb.id = m.mailbox_id WHERE m.id IN (?"+strings.Repeat(",?", len(chunk)-1)+")", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var s Snapshot
			if err := rows.Scan(&s.ID, &s.UID, &s.UIDValidity, &s.MsgID, &s.Digest, &s.Mailbox, &s.IMAPName); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out = append(out, s)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	slices.SortStableFunc(out, func(a, b Snapshot) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

func ServerMsgID(header []byte) string {
	value, _ := mime.Compat32Header(header, "Message-ID")
	return features.NormalizeMsgID(value)
}

type boxKey struct {
	imapName string
	validity uint32
}

func Verify(ctx context.Context, q Querier, client mail.Client, account string, snaps []Snapshot) (ok []Verified, skipped []Skip, err error) {
	var order []boxKey
	survivors := map[boxKey][]Snapshot{}
	for _, snap := range snaps {
		var gone sql.NullFloat64
		var uid, validity int64
		var digest, imapName string
		var unresolved bool
		err := q.QueryRowContext(ctx, "SELECT m.gone_at, m.uid, m.uidvalidity, m.digest, mb.imap_name, "+UnresolvedSQL+" FROM messages m "+
			"JOIN mailboxes mb ON mb.id = m.mailbox_id WHERE m.id = ?", snap.ID).Scan(&gone, &uid, &validity, &digest, &imapName, &unresolved)
		reason := ""
		switch {
		case errors.Is(err, sql.ErrNoRows) || err == nil && gone.Valid:
			reason = ReasonGone
		case err != nil:
			return nil, nil, err
		case validity == 0 || uid != int64(snap.UID) || validity != int64(snap.UIDValidity) || imapName != snap.IMAPName:
			reason = ReasonMoved
		case digest != snap.Digest:
			reason = ReasonChanged
		case unresolved:
			reason = ReasonUnresolved
		}
		if reason != "" {
			skipped = append(skipped, Skip{snap.ID, reason})
			continue
		}
		key := boxKey{snap.IMAPName, snap.UIDValidity}
		if _, seen := survivors[key]; !seen {
			order = append(order, key)
		}
		survivors[key] = append(survivors[key], snap)
	}
	items := mail.FetchItems{HeaderOnly: true, Labels: account == config.Gmail}
	for _, key := range order {
		group := survivors[key]
		st, err := client.Status(ctx, key.imapName)
		if err != nil {
			return nil, nil, err
		}
		if st.UIDValidity == 0 || st.UIDValidity != key.validity {
			for _, s := range group {
				skipped = append(skipped, Skip{s.ID, ReasonUIDValidity})
			}
			continue
		}
		if err := client.Select(ctx, key.imapName); err != nil {
			return nil, nil, err
		}
		uids := make([]uint32, len(group))
		for i, s := range group {
			uids[i] = s.UID
		}
		present := map[uint32]bool{}
		for _, chunk := range Chunks(uids, Batch) {
			set := mail.UIDs(chunk...)
			found, err := client.UIDSearch(ctx, mail.SearchCriteria{UID: &set})
			if err != nil {
				return nil, nil, err
			}
			for _, u := range found {
				present[u] = true
			}
		}
		var live []Snapshot
		var liveUIDs []uint32
		for _, s := range group {
			if present[s.UID] {
				live = append(live, s)
				liveUIDs = append(liveUIDs, s.UID)
			} else {
				skipped = append(skipped, Skip{s.ID, ReasonMissing})
			}
		}
		fetched := map[uint32]mail.Message{}
		for _, chunk := range Chunks(liveUIDs, Batch) {
			msgs, err := client.UIDFetch(ctx, mail.UIDs(chunk...), items)
			if err != nil {
				return nil, nil, err
			}
			for _, m := range msgs {
				fetched[m.UID] = m
			}
		}
		for _, snap := range live {
			item, found := fetched[snap.UID]
			if !found {
				skipped = append(skipped, Skip{snap.ID, ReasonMissing})
				continue
			}
			if snap.MsgID != "" {
				if ServerMsgID(item.Header) != snap.MsgID {
					skipped = append(skipped, Skip{snap.ID, ReasonMsgID})
					continue
				}
			}
			labels := item.Labels
			if labels == nil {
				labels = []string{}
			}
			ok = append(ok, Verified{Snapshot: snap, Flags: item.Flags, Labels: labels})
		}
	}
	slices.SortStableFunc(ok, func(a, b Verified) int { return cmp.Compare(a.ID, b.ID) })
	return ok, skipped, nil
}

type Group struct {
	IMAPName    string
	UIDValidity uint32
	Items       []Verified
}

func GroupByMailbox(items []Verified) []Group {
	var out []Group
	index := map[boxKey]int{}
	for _, item := range items {
		key := boxKey{item.IMAPName, item.UIDValidity}
		i, ok := index[key]
		if !ok {
			i = len(out)
			index[key] = i
			out = append(out, Group{IMAPName: item.IMAPName, UIDValidity: item.UIDValidity})
		}
		out[i].Items = append(out[i].Items, item)
	}
	return out
}

func TrashMailbox(ctx context.Context, client mail.Client, account string) (string, error) {
	boxes, err := client.ListMailboxes(ctx)
	if err != nil {
		return "", err
	}
	found := ""
	for _, info := range boxes {
		for _, f := range info.Flags {
			if py.Lower(f) == py.Lower(TrashLabel) {
				return info.IMAPName, nil
			}
		}
		if account != config.Gmail && found == "" && trashNames[py.Casefold(info.Name)] {
			found = info.IMAPName
		}
	}
	if found == "" {
		return "", Error("no Trash mailbox found")
	}
	return found, nil
}

func EnsureFolder(ctx context.Context, client mail.Client, imapName string) (bool, error) {
	boxes, err := client.ListMailboxes(ctx)
	if err != nil {
		return false, err
	}
	for _, info := range boxes {
		if info.IMAPName == imapName {
			return false, nil
		}
	}
	if err := client.CreateMailbox(ctx, imapName); err != nil {
		return false, err
	}
	return true, client.SubscribeMailbox(ctx, imapName)
}

func DeleteFolderIfEmpty(ctx context.Context, client mail.Client, imapName string) (bool, error) {
	st, err := client.Status(ctx, imapName)
	if err != nil {
		return false, err
	}
	if st.Messages != 0 {
		return false, nil
	}
	return true, client.DeleteMailbox(ctx, imapName)
}

func SelectRW(ctx context.Context, client mail.Client, imapName string, validity uint32) error {
	got, err := client.SelectRW(ctx, imapName)
	if err != nil {
		return err
	}
	if validity != 0 && got != validity {
		return ErrValidityChanged
	}
	return nil
}

func CopyUIDs(ctx context.Context, client mail.Client, destIMAP string, uids []uint32) (*mail.CopyUID, error) {
	return client.Copy(ctx, mail.UIDs(uids...), destIMAP)
}

func MoveUIDs(ctx context.Context, client mail.Client, srcIMAP, destIMAP string, validity uint32, uids []uint32,
	moved func(batch []uint32, copied *mail.CopyUID) error) error {
	if !client.HasCapability("UIDPLUS") {
		return Error("server lacks UIDPLUS: refusing to move")
	}
	if err := SelectRW(ctx, client, srcIMAP, validity); err != nil {
		return err
	}
	for _, batch := range Chunks(uids, Batch) {
		copied, err := CopyUIDs(ctx, client, destIMAP, batch)
		if err != nil {
			if _, unknown := errors.AsType[*mail.WriteInterrupted](err); unknown {
				return &PartialMove{Err: err, Unconfirmed: true}
			}
			return err
		}
		set := mail.UIDs(batch...)
		if err := client.StoreFlags(ctx, set, true, Deleted); err != nil {
			return &PartialMove{Copied: copied, Err: err}
		}
		if err := client.UIDExpunge(ctx, set); err != nil {
			return &PartialMove{Copied: copied, Err: err}
		}
		if err := moved(batch, copied); err != nil {
			return err
		}
	}
	return nil
}

func StoreLabels(ctx context.Context, client mail.Client, imapName string, validity uint32, uids []uint32, add, remove []string) error {
	if err := SelectRW(ctx, client, imapName, validity); err != nil {
		return err
	}
	for _, batch := range Chunks(uids, Batch) {
		set := mail.UIDs(batch...)
		if len(add) > 0 {
			if err := client.StoreLabels(ctx, set, true, add...); err != nil {
				return err
			}
		}
		if len(remove) > 0 {
			if err := client.StoreLabels(ctx, set, false, remove...); err != nil {
				return err
			}
		}
	}
	return nil
}

func StoreFlag(ctx context.Context, client mail.Client, imapName string, validity uint32, uids []uint32, flag string, add bool) error {
	if err := SelectRW(ctx, client, imapName, validity); err != nil {
		return err
	}
	for _, batch := range Chunks(uids, Batch) {
		if err := client.StoreFlags(ctx, mail.UIDs(batch...), add, flag); err != nil {
			return err
		}
	}
	return nil
}

func ArchiveUIDs(ctx context.Context, client mail.Client, imapName string, validity uint32, uids []uint32) error {
	return StoreLabels(ctx, client, imapName, validity, uids, nil, []string{InboxLabel})
}

type Relabel struct {
	Add    []string
	Remove []string
	Items  []Verified
}

func RelabelPlan(items []Verified, add, remove []string) []Relabel {
	var out []Relabel
	index := map[string]int{}
	for _, item := range items {
		wantAdd := []string{}
		for _, l := range add {
			if !slices.Contains(item.Labels, l) {
				wantAdd = append(wantAdd, l)
			}
		}
		wantRemove := []string{}
		for _, l := range remove {
			if slices.Contains(item.Labels, l) {
				wantRemove = append(wantRemove, l)
			}
		}
		key := py.JSONDumps([]any{wantAdd, wantRemove})
		i, ok := index[key]
		if !ok {
			i = len(out)
			index[key] = i
			out = append(out, Relabel{Add: wantAdd, Remove: wantRemove})
		}
		out[i].Items = append(out[i].Items, item)
	}
	return out
}
