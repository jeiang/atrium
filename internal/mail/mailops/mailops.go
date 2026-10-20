// Package mailops is mailops.py: the mailbox write helpers every applied proposal, automatic action and undo
// shares, and the pre-write verification of message snapshots against the live mailbox. Every write goes
// through the gated mail.Client; a move is COPY, STORE \Deleted and UID EXPUNGE.
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
)

var trashNames = map[string]bool{"deleted messages": true, "deleted items": true, "trash": true, "bin": true}

// Error is MailOpError.
type Error string

func (e Error) Error() string { return string(e) }

func (Error) PyClass() string { return "MailOpError" }

func (Error) PyModule() string { return "atrium.mailops" }

// ErrValidityChanged is the refusal of a write whose mailbox reports another UIDVALIDITY than the verified one;
// nothing was sent.
var ErrValidityChanged = Error("UIDVALIDITY changed")

// PartialMove is a batch that was copied but not expunged: Copied is what COPYUID reported, nil without it.
type PartialMove struct {
	Copied *mail.CopyUID
	Err    error
	// Unconfirmed is a COPY whose reply never came: the destination may hold the copy.
	Unconfirmed bool
}

// PartialPrefix is in the text of a PartialMove: a retry of the same move would copy the batch a second time.
// UnknownPrefix starts the ledger text of a write a cancelled context cut off: the server may have executed it.
const UnknownPrefix = "cancelled, write outcome unknown"

// Blocks reports whether an undo's recorded failure text forbids repeating the undo.
func Blocks(text string) bool {
	return strings.Contains(text, PartialPrefix) || strings.Contains(text, UnknownPrefix)
}

// OutcomeUnknown reports whether err is a write the server may or may not have executed: a command that was sent
// and never answered, alone or as the COPY of a PartialMove.
func OutcomeUnknown(err error) bool {
	if _, ok := errors.AsType[*mail.WriteInterrupted](err); ok {
		return true
	}
	partial, ok := errors.AsType[*PartialMove](err)
	return ok && partial.Unconfirmed
}

// UnknownText is the ledger text of a failed write: err's text, led by UnknownPrefix when its outcome is unknown
// and the text does not already say so.
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

// Opener connects an account's mail client.
type Opener func(ctx context.Context, account string) (mail.Client, error)

// Querier is the read side of *sql.DB and *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Session is `with session(cfg, account) as client`: fn runs on a connected client that is closed after.
func Session(ctx context.Context, open Opener, account string, fn func(mail.Client) error) error {
	client, err := open(ctx, account)
	if err != nil {
		return err
	}
	defer client.Close()
	return fn(client)
}

// Chunks is chunks(items, size).
func Chunks[T any](items []T, size int) [][]T {
	var out [][]T
	for start := 0; start < len(items); start += size {
		out = append(out, items[start:min(start+size, len(items))])
	}
	return out
}

// Snapshot is one message as a proposal or action saw it.
type Snapshot struct {
	ID          int64  `json:"id"`
	UID         uint32 `json:"uid"`
	UIDValidity uint32 `json:"uidvalidity"`
	MsgID       string `json:"msgid"`
	Digest      string `json:"digest"`
	Mailbox     string `json:"mailbox"`
	IMAPName    string `json:"imap_name"`
}

// Dict is the snapshot as Python stores it in a payload; encoding/json reads it back through the tags.
func (s Snapshot) Dict() py.Dict {
	return py.Dict{
		{Key: "id", Value: s.ID}, {Key: "uid", Value: s.UID}, {Key: "uidvalidity", Value: s.UIDValidity},
		{Key: "msgid", Value: s.MsgID}, {Key: "digest", Value: s.Digest}, {Key: "mailbox", Value: s.Mailbox},
		{Key: "imap_name", Value: s.IMAPName},
	}
}

// Verified is a snapshot verify found unchanged, with the flags and labels the server has now.
type Verified struct {
	Snapshot
	Flags  []string
	Labels []string
}

// Skip is one snapshot verify dropped and why.
type Skip struct {
	ID     int64
	Reason string
}

func (s Skip) Dict() py.Dict {
	return py.Dict{{Key: "id", Value: s.ID}, {Key: "reason", Value: s.Reason}}
}

// Snapshots is snapshots(conn, ids), by id.
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

// ServerMsgID is server_msgid: the normalized Message-ID of a fetched header as policy.compat32 reads it.
func ServerMsgID(header []byte) string {
	value, _ := mime.Compat32Header(header, "Message-ID")
	return features.NormalizeMsgID(value)
}

type boxKey struct {
	imapName string
	validity uint32
}

// Verify is verify: each snapshot is checked against the database, then against the live mailbox; ok is by id,
// skipped in the order Python found them.
func Verify(ctx context.Context, q Querier, client mail.Client, account string, snaps []Snapshot) (ok []Verified, skipped []Skip, err error) {
	var order []boxKey
	survivors := map[boxKey][]Snapshot{}
	for _, snap := range snaps {
		var gone sql.NullFloat64
		var uid, validity int64
		var digest, imapName string
		err := q.QueryRowContext(ctx, "SELECT m.gone_at, m.uid, m.uidvalidity, m.digest, mb.imap_name FROM messages m "+
			"JOIN mailboxes mb ON mb.id = m.mailbox_id WHERE m.id = ?", snap.ID).Scan(&gone, &uid, &validity, &digest, &imapName)
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

// Group is the verified items of one mailbox at one UIDVALIDITY.
type Group struct {
	IMAPName    string
	UIDValidity uint32
	Items       []Verified
}

// GroupByMailbox is group_by_mailbox, groups in the order their first item appears.
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

// TrashMailbox is trash_mailbox: the mailbox flagged \Trash, else (not on Gmail) the first one named like a
// trash folder.
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

// EnsureFolder is ensure_folder: it creates and subscribes imapName unless it is listed; true when created.
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

// DeleteFolderIfEmpty is delete_folder_if_empty: true when imapName had no message and was deleted.
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

// SelectRW opens imapName read-write and refuses when the server's UIDVALIDITY is not validity, before any write
// is sent. A validity of 0 is an undo plan that recorded none; every other caller passes the verified value.
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

// CopyUIDs is copy_uids: the COPYUID mapping, nil when the server sent none.
func CopyUIDs(ctx context.Context, client mail.Client, destIMAP string, uids []uint32) (*mail.CopyUID, error) {
	return client.Copy(ctx, mail.UIDs(uids...), destIMAP)
}

// MoveUIDs is move_uids: per batch of Batch UIDs a COPY, STORE +FLAGS.SILENT \Deleted and UID EXPUNGE, then
// moved with the batch and its COPYUID. A server without UIDPLUS is refused before anything is sent; a batch
// copied but not expunged is a *PartialMove, and so is a COPY whose reply never came (Copied is nil: the
// destination may hold the copy, so it is never sent again).
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

// StoreLabels is store_labels: per batch, +X-GM-LABELS add then -X-GM-LABELS remove.
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

// StoreFlag is store_flag: per batch, +FLAGS.SILENT or -FLAGS.SILENT flag.
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

// ArchiveUIDs is archive_uids: the Gmail \Inbox label comes off.
func ArchiveUIDs(ctx context.Context, client mail.Client, imapName string, validity uint32, uids []uint32) error {
	return StoreLabels(ctx, client, imapName, validity, uids, nil, []string{InboxLabel})
}

// Relabel is one relabel_plan group: the labels still to add and to remove for its items.
type Relabel struct {
	Add    []string
	Remove []string
	Items  []Verified
}

// RelabelPlan is relabel_plan, groups in the order their first item appears.
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
