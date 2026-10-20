package applier

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/features"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/rules"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/proposals/executors"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/safety"
)

var log = logx.Logger("atrium.applier")

const (
	deletionNotBlocked = "NOT EXISTS (SELECT 1 FROM auto_actions a WHERE a.account = m.account AND a.digest = m.digest " +
		"AND ((a.kind = 'trash' AND a.ok IS NOT 0) OR a.undone_ts IS NOT NULL)) " +
		"AND (SELECT COUNT(*) FROM auto_actions a WHERE a.account = m.account AND a.digest = m.digest " +
		"AND a.kind = 'trash' AND a.ok = 0) < :attempts"
	noCorrection = "NOT EXISTS (SELECT 1 FROM corrections c WHERE c.message_id = m.id)"

	inboxCandidates = `SELECT m.id, m.digest, d.id, d.action, d.dest
FROM messages m JOIN decisions d ON d.id = (SELECT MAX(x.id) FROM decisions x WHERE x.message_id = m.id)
WHERE m.account = :account AND m.in_inbox = 1 AND m.seen = 1 AND m.flagged = 0 AND m.sent = 0
AND m.gone_at IS NULL AND (d.action = 'delete' OR (d.action = 'file' AND d.dest IS NOT NULL))
AND m.id NOT IN (SELECT message_id FROM live_baseline)
AND ` + live.NoNewCorrection + ` AND ` + live.NotBlocked + `
ORDER BY m.id`
	correctionCandidates = `SELECT m.id, m.digest, m.location, m.labels, c.folder
FROM corrections c JOIN messages m ON m.id = c.message_id
WHERE c.account = :account AND c.ts >= :golive AND m.gone_at IS NULL AND m.sent = 0
AND NOT EXISTS (SELECT 1 FROM auto_actions a WHERE a.message_id = m.id AND a.kind = 'correct'
AND a.dest = c.folder AND a.ok IS NOT 0)
AND (SELECT COUNT(*) FROM auto_actions a WHERE a.message_id = m.id AND a.kind = 'correct'
AND a.dest = c.folder AND a.ok = 0) < :attempts
ORDER BY m.id`
	flagCandidates = `SELECT m.id, m.digest FROM messages m JOIN flag_decisions f ON f.message_id = m.id
WHERE m.account = :account AND m.in_inbox = 1 AND m.seen = 0 AND m.flagged = 0 AND m.sent = 0
AND m.gone_at IS NULL AND f.contacts_match = 1
AND NOT EXISTS (SELECT 1 FROM auto_actions a WHERE a.message_id = m.id AND a.kind = 'flag' AND a.ok IS NOT 0)
AND (SELECT COUNT(*) FROM auto_actions a WHERE a.message_id = m.id AND a.kind = 'flag' AND a.ok = 0) < :attempts
ORDER BY m.id`
	deletionCandidates = `SELECT ` + rules.MessageColumns + `, m.seen, m.digest FROM messages m
WHERE m.account = :account AND m.gone_at IS NULL AND m.sent = 0 AND m.flagged = 0
AND m.mailbox_id IN (SELECT mb.id FROM mailboxes mb WHERE mb.role NOT IN ('drafts', 'junk', 'sent', 'system', 'trash'))
AND ` + noCorrection + ` AND ` + deletionNotBlocked + `
ORDER BY m.id`
	priorLabel = "SELECT dest FROM auto_actions WHERE account = ? AND digest = ? AND kind IN ('file', 'correct') " +
		"AND ok = 1 AND undone_ts IS NULL ORDER BY id DESC LIMIT 1"
)

const alreadyThere = "already there"

type check func(account string, item mailops.Verified) string

type Intent struct {
	ID         int64
	Digest     string
	Kind       string
	check      check
	Dest       *string
	DecisionID *int64
	Remove     []string
}

type Budget struct {
	Left     int
	Deferred int
}

func take[T any](b *Budget, rows []T, what string) []T {
	kept := rows[:min(b.Left, len(rows))]
	if extra := len(rows) - len(kept); extra > 0 {
		b.Deferred += extra
		log.Warn(fmt.Sprintf("write cap reached: deferring %d %s to a later pass", extra, what))
	}
	b.Left -= len(kept)
	return kept
}

type Outcome struct {
	Written int
	Skipped int
	Failed  int
	ByKind  py.Dict
	Ran     bool
}

func (o *Outcome) count(kind string, n int) {
	o.Written += n
	o.ByKind = o.ByKind.Set(kind, o.Kind(kind)+n)
}

func (o Outcome) Kind(kind string) int {
	n, _ := o.ByKind.Get(kind)
	v, _ := n.(int)
	return v
}

func inInbox(account string, item mailops.Verified) bool {
	if account == config.Gmail {
		return slices.Contains(item.Labels, mailops.InboxLabel)
	}
	return py.Casefold(item.IMAPName) == py.Casefold(config.Inbox)
}

func leftInbox(account string, item mailops.Verified) string {
	if inInbox(account, item) {
		return ""
	}
	return "left the inbox"
}

func readInbox(account string, item mailops.Verified) string {
	if !slices.Contains(item.Flags, mailops.Seen) {
		return "unread"
	}
	if slices.Contains(item.Flags, mailops.Flagged) {
		return "flagged"
	}
	return leftInbox(account, item)
}

func unreadInbox(account string, item mailops.Verified) string {
	if slices.Contains(item.Flags, mailops.Seen) {
		return "read"
	}
	if slices.Contains(item.Flags, mailops.Flagged) {
		return "flagged"
	}
	return leftInbox(account, item)
}

func unflagged(_ string, item mailops.Verified) string {
	if slices.Contains(item.Flags, mailops.Flagged) {
		return "flagged"
	}
	return ""
}

func unflaggedRead(_ string, item mailops.Verified) string {
	if slices.Contains(item.Flags, mailops.Flagged) {
		return "flagged"
	}
	if slices.Contains(item.Flags, mailops.Seen) {
		return ""
	}
	return "unread"
}

func anywhere(string, mailops.Verified) string { return "" }

func protectedNames(ctx context.Context, client mail.Client) (map[string]bool, error) {
	boxes, err := client.ListMailboxes(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, info := range boxes {
		if slices.ContainsFunc(info.Flags, mail.IsSystemFlag) || mail.IsSystemName(info.Name) {
			names[info.IMAPName] = true
		}
	}
	return names, nil
}

func safeLabel(name string) bool {
	if _, err := executors.ValidName(name); err != nil {
		return false
	}
	return !strings.HasPrefix(name, "\\") && !mail.IsSystemName(name) && !strings.HasPrefix(name, "[Gmail]")
}

type Applier struct {
	Live   *live.Live
	Open   mailops.Opener
	Resync func(ctx context.Context, account string) error
	Cap    int
}

func New(l *live.Live, open mailops.Opener, syncer *mailsync.Syncer) *Applier {
	return &Applier{Live: l, Open: open, Resync: func(ctx context.Context, account string) error {
		return mailops.Session(ctx, open, account, func(client mail.Client) error {
			_, err := syncer.Account(ctx, account, client)
			return err
		})
	}}
}

func (a *Applier) now() float64 {
	if a.Live.Now != nil {
		return a.Live.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (a *Applier) cap() int {
	if a.Cap != 0 {
		return a.Cap
	}
	return config.WriteCap
}

func (a *Applier) query(ctx context.Context, account, query string) (*sql.Rows, error) {
	p, err := live.QueryParams(ctx, a.Live.DB.Write, account)
	if err != nil {
		return nil, err
	}
	return a.Live.DB.Write.QueryContext(ctx, query, p.Args()...)
}

func beforeOf(item mailops.Verified) py.Dict {
	flags := item.Flags
	if flags == nil {
		flags = []string{}
	}
	return py.Dict{
		{Key: "imap_name", Value: item.IMAPName}, {Key: "uidvalidity", Value: item.UIDValidity}, {Key: "uid", Value: item.UID},
		{Key: "msgid", Value: item.MsgID}, {Key: "flags", Value: flags}, {Key: "labels", Value: item.Labels},
	}
}

func moveUndo(account string, item mailops.Verified, destIMAP string, destValidity, destUID any) py.Dict {
	return py.Dict{
		{Key: "op", Value: live.OpMoveBack}, {Key: "account", Value: account}, {Key: "src_imap", Value: item.IMAPName},
		{Key: "dest_imap", Value: destIMAP}, {Key: "dest_uidvalidity", Value: destValidity},
		{Key: "items", Value: []any{py.Dict{
			{Key: "id", Value: item.ID}, {Key: "msgid", Value: item.MsgID}, {Key: "uid", Value: item.UID}, {Key: "dest_uid", Value: destUID},
		}}},
	}
}

func copied(c *mail.CopyUID, uid uint32) (validity, destUID any) {
	if c == nil {
		return nil, nil
	}
	if u, ok := c.Mapping[uid]; ok {
		return c.UIDValidity, u
	}
	return c.UIDValidity, nil
}

func unique(lists ...[]string) []string {
	out := []string{}
	for _, list := range lists {
		for _, s := range list {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

type entry struct {
	item   mailops.Verified
	intent *Intent
	aid    int64
}

type group struct {
	run      func(context.Context, mail.Client, string, *group, *Outcome) error
	imapName string
	validity uint32
	dest     string
	added    []string
	removed  []string
	entries  []entry
}

func (g *group) byUID() (map[uint32]entry, []uint32) {
	by := make(map[uint32]entry, len(g.entries))
	for _, e := range g.entries {
		by[e.item.UID] = e
	}
	uids := make([]uint32, 0, len(by))
	for uid := range by {
		uids = append(uids, uid)
	}
	slices.Sort(uids)
	return by, uids
}

type groups struct {
	order []*group
	index map[string]*group
}

func (gs *groups) add(key string, mk func() *group, e entry) {
	if gs.index == nil {
		gs.index = map[string]*group{}
	}
	g, ok := gs.index[key]
	if !ok {
		g = mk()
		gs.index[key] = g
		gs.order = append(gs.order, g)
	}
	g.entries = append(g.entries, e)
}

type ledgerError struct{ err error }

func (e ledgerError) Error() string { return e.err.Error() }

func (a *Applier) discard(ctx context.Context, by map[uint32]entry, uids []uint32) error {
	for _, uid := range uids {
		if err := a.Live.Discard(ctx, by[uid].aid); err != nil {
			return err
		}
	}
	return nil
}

func (a *Applier) moveGroup(ctx context.Context, client mail.Client, account string, g *group, out *Outcome) error {
	by, remaining := g.byUID()
	err := mailops.MoveUIDs(ctx, client, g.imapName, g.dest, g.validity, remaining, func(batch []uint32, c *mail.CopyUID) error {
		for _, uid := range batch {
			e := by[uid]
			validity, destUID := copied(c, uid)
			after := py.Dict{{Key: "imap_name", Value: g.dest}, {Key: "uidvalidity", Value: validity}, {Key: "uid", Value: destUID}}
			if err := a.Live.Finish(ctx, e.aid, after, moveUndo(account, e.item, g.dest, validity, destUID), nil); err != nil {
				return ledgerError{err}
			}
			out.count(e.intent.Kind, 1)
		}
		remaining = remaining[len(batch):]
		return nil
	})
	if err == nil {
		return nil
	}
	if le, ok := errors.AsType[ledgerError](err); ok {
		return le.err
	}
	failing, rest := remaining[:min(mailops.Batch, len(remaining))], remaining[min(mailops.Batch, len(remaining)):]
	if partial, ok := errors.AsType[*mailops.PartialMove](err); ok {
		for _, uid := range failing {
			validity, destUID := copied(partial.Copied, uid)
			after := py.Dict{{Key: "imap_name", Value: g.dest}, {Key: "uidvalidity", Value: validity}, {Key: "uid", Value: destUID}}
			if err := a.Live.Hold(ctx, by[uid].aid, partial.Error(), after); err != nil {
				return err
			}
		}
		out.Failed += len(failing)
		if err := a.discard(ctx, by, rest); err != nil {
			return err
		}
		log.Error(fmt.Sprintf("%s: partial move from %s to %s: %s", account, g.imapName, g.dest, partial.Error()))
		return nil
	}
	for _, uid := range failing {
		if err := a.Live.Fail(ctx, by[uid].aid, py.ExceptionText(err)); err != nil {
			return err
		}
	}
	out.Failed += len(failing)
	if err := a.discard(ctx, by, rest); err != nil {
		return err
	}
	log.Error(fmt.Sprintf("%s: move from %s to %s failed: %s", account, g.imapName, g.dest, err.Error()))
	return nil
}

func observedLabels(ctx context.Context, client mail.Client, imapName string, uids []uint32) map[uint32][]string {
	fail := func(err error) map[uint32][]string {
		log.Error("could not re-read labels after a failed store", logx.Err, err.Error())
		return nil
	}
	if err := client.Select(ctx, imapName); err != nil {
		return fail(err)
	}
	msgs, err := client.UIDFetch(ctx, mail.UIDs(uids...), mail.FetchItems{Labels: true})
	if err != nil {
		return fail(err)
	}
	out := make(map[uint32][]string, len(msgs))
	for _, m := range msgs {
		out[m.UID] = m.Labels
	}
	return out
}

func labelUndo(g *group, uid uint32, add, remove []string) py.Dict {
	return py.Dict{
		{Key: "op", Value: live.OpGmailLabels}, {Key: "imap_name", Value: g.imapName}, {Key: "uidvalidity", Value: g.validity},
		{Key: "uids", Value: []uint32{uid}}, {Key: "add", Value: add}, {Key: "remove", Value: remove},
	}
}

func (a *Applier) labelGroup(ctx context.Context, client mail.Client, account string, g *group, out *Outcome) error {
	by, uids := g.byUID()
	for _, chunk := range mailops.Chunks(uids, mailops.Batch) {
		if err := mailops.StoreLabels(ctx, client, g.imapName, g.validity, chunk, g.added, g.removed); err != nil {
			text := py.ExceptionText(err)
			if errors.Is(err, mailops.ErrValidityChanged) {
				for _, uid := range chunk {
					if err := a.Live.Fail(ctx, by[uid].aid, text); err != nil {
						return err
					}
				}
				out.Failed += len(chunk)
				if err := a.discard(ctx, by, uids[slices.Index(uids, chunk[len(chunk)-1])+1:]); err != nil {
					return err
				}
				log.Error(fmt.Sprintf("%s: relabel in %s refused: %s", account, g.imapName, err.Error()))
				return nil
			}
			actual := observedLabels(ctx, client, g.imapName, chunk)
			for _, uid := range chunk {
				e := by[uid]
				have, known := actual[uid]
				didAdd, didRemove := []string{}, []string{}
				for _, l := range g.added {
					if known && slices.Contains(have, l) && !slices.Contains(e.item.Labels, l) {
						didAdd = append(didAdd, l)
					}
				}
				for _, l := range g.removed {
					if known && !slices.Contains(have, l) && slices.Contains(e.item.Labels, l) {
						didRemove = append(didRemove, l)
					}
				}
				if len(didAdd) == 0 && len(didRemove) == 0 {
					if err := a.Live.Fail(ctx, e.aid, text); err != nil {
						return err
					}
					out.Failed++
					continue
				}
				priorAdd, priorRemove, err := a.Live.AbsorbLabelUndo(ctx, e.item.ID, e.intent.Kind)
				if err != nil {
					return err
				}
				undo := labelUndo(g, uid, unique(didRemove, priorAdd), unique(didAdd, priorRemove))
				after := py.Dict{{Key: "add", Value: didAdd}, {Key: "remove", Value: didRemove}}
				if slices.Equal(didAdd, g.added) && slices.Equal(didRemove, g.removed) {
					if err := a.Live.Finish(ctx, e.aid, after, undo, new("completed after: "+text)); err != nil {
						return err
					}
					out.count(e.intent.Kind, 1)
				} else {
					if err := a.Live.Partial(ctx, e.aid, "partial: "+text, after, undo); err != nil {
						return err
					}
					out.Failed++
				}
			}
			if err := a.discard(ctx, by, uids[slices.Index(uids, chunk[len(chunk)-1])+1:]); err != nil {
				return err
			}
			log.Error(fmt.Sprintf("%s: relabel in %s failed: %s", account, g.imapName, err.Error()))
			return nil
		}
		for _, uid := range chunk {
			e := by[uid]
			after := py.Dict{{Key: "add", Value: g.added}, {Key: "remove", Value: g.removed}}
			priorAdd, priorRemove, err := a.Live.AbsorbLabelUndo(ctx, e.item.ID, e.intent.Kind)
			if err != nil {
				return err
			}
			undo := labelUndo(g, uid, unique(g.removed, priorAdd), unique(g.added, priorRemove))
			if err := a.Live.Finish(ctx, e.aid, after, undo, nil); err != nil {
				return err
			}
			out.count(e.intent.Kind, 1)
		}
	}
	return nil
}

func locateInTrash(ctx context.Context, client mail.Client, trash string, item mailops.Verified) (validity, uid uint32, ok bool) {
	fail := func(err error) (uint32, uint32, bool) {
		log.Error("could not look for the message in Trash after a failed copy", logx.Err, err.Error())
		return 0, 0, false
	}
	st, err := client.Status(ctx, trash)
	if err != nil {
		return fail(err)
	}
	if err := client.Select(ctx, trash); err != nil {
		return fail(err)
	}
	var found []uint32
	if item.MsgID != "" {
		if found, err = client.UIDSearch(ctx, mail.SearchCriteria{HeaderName: "Message-ID", HeaderValue: item.MsgID}); err != nil {
			return fail(err)
		}
	}
	var same []uint32
	if len(found) > 0 {
		msgs, err := client.UIDFetch(ctx, mail.UIDs(found...), mail.FetchItems{Labels: true, Header: true, Body: true})
		if err != nil {
			return fail(err)
		}
		for _, m := range msgs {
			if features.ContentDigest(m.Header, m.Text) == item.Digest {
				same = append(same, m.UID)
			}
		}
	}
	if len(same) != 1 {
		return 0, 0, false
	}
	return st.UIDValidity, same[0], true
}

func untrashUndo(trash string, validity, uid any) py.Dict {
	return py.Dict{
		{Key: "op", Value: live.OpGmailUntrash}, {Key: "trash_imap", Value: trash}, {Key: "dest_uidvalidity", Value: validity},
		{Key: "uids", Value: []any{uid}},
	}
}

func (a *Applier) gmailTrashGroup(ctx context.Context, client mail.Client, account string, g *group, out *Outcome) error {
	by, uids := g.byUID()
	for _, chunk := range mailops.Chunks(uids, mailops.Batch) {
		var c *mail.CopyUID
		err := mailops.SelectRW(ctx, client, g.imapName, g.validity)
		if err == nil {
			c, err = mailops.CopyUIDs(ctx, client, g.dest, chunk)
		}
		if err != nil {
			text := py.ExceptionText(err)
			_, refused := errors.AsType[safety.WriteRefused](err)
			refused = refused || errors.Is(err, mailops.ErrValidityChanged)
			_, unknown := errors.AsType[*mail.WriteInterrupted](err)
			for _, uid := range chunk {
				e := by[uid]
				var validity, destUID uint32
				found := false
				if !refused {
					validity, destUID, found = locateInTrash(ctx, client, g.dest, e.item)
				}
				if !found && unknown {
					if err := a.Live.Hold(ctx, e.aid, text, nil); err != nil {
						return err
					}
					out.Failed++
					continue
				}
				if !found {
					if err := a.Live.Fail(ctx, e.aid, text); err != nil {
						return err
					}
					out.Failed++
					continue
				}
				after := py.Dict{{Key: "imap_name", Value: g.dest}, {Key: "uidvalidity", Value: validity}, {Key: "uid", Value: destUID}}
				if err := a.Live.Finish(ctx, e.aid, after, untrashUndo(g.dest, validity, destUID), new("partial: "+text)); err != nil {
					return err
				}
				out.count(e.intent.Kind, 1)
			}
			if err := a.discard(ctx, by, uids[slices.Index(uids, chunk[len(chunk)-1])+1:]); err != nil {
				return err
			}
			log.Error(fmt.Sprintf("%s: trash copy failed: %s", account, err.Error()))
			return nil
		}
		var destValidity any
		var mapping map[uint32]uint32
		if c != nil {
			destValidity, mapping = c.UIDValidity, c.Mapping
		}
		for _, uid := range chunk {
			e := by[uid]
			var destUID any
			if u, ok := mapping[uid]; ok {
				destUID = u
			} else if v, u, found := locateInTrash(ctx, client, g.dest, e.item); found {
				destValidity, destUID = v, u
			}
			var undo any
			if destUID != nil {
				undo = untrashUndo(g.dest, destValidity, destUID)
			}
			after := py.Dict{{Key: "imap_name", Value: g.dest}, {Key: "uidvalidity", Value: destValidity}, {Key: "uid", Value: destUID}}
			if err := a.Live.Finish(ctx, e.aid, after, undo, nil); err != nil {
				return err
			}
			out.count(e.intent.Kind, 1)
		}
	}
	return nil
}

func (a *Applier) flagGroup(ctx context.Context, client mail.Client, account string, g *group, out *Outcome) error {
	by, uids := g.byUID()
	for _, chunk := range mailops.Chunks(uids, mailops.Batch) {
		if err := mailops.StoreFlag(ctx, client, g.imapName, g.validity, chunk, mailops.Flagged, true); err != nil {
			for _, uid := range chunk {
				if err := a.Live.Fail(ctx, by[uid].aid, py.ExceptionText(err)); err != nil {
					return err
				}
			}
			out.Failed += len(chunk)
			if err := a.discard(ctx, by, uids[slices.Index(uids, chunk[len(chunk)-1])+1:]); err != nil {
				return err
			}
			log.Error(fmt.Sprintf("%s: flagging in %s failed: %s", account, g.imapName, err.Error()))
			return nil
		}
		for _, uid := range chunk {
			e := by[uid]
			undo := py.Dict{
				{Key: "op", Value: live.OpUnflag}, {Key: "account", Value: account}, {Key: "imap_name", Value: g.imapName},
				{Key: "uidvalidity", Value: g.validity}, {Key: "uids", Value: []uint32{uid}},
			}
			if err := a.Live.Finish(ctx, e.aid, py.Dict{{Key: "flag", Value: mailops.Flagged}}, undo, nil); err != nil {
				return err
			}
			out.count(e.intent.Kind, 1)
		}
	}
	return nil
}

func boxKey(parts ...any) string { return py.JSONDumps(parts) }

func (a *Applier) execute(ctx context.Context, client mail.Client, account string, intents []Intent, out *Outcome) error {
	if len(intents) == 0 {
		return nil
	}
	byID := map[int64]*Intent{}
	var ids []int64
	for i := range intents {
		if _, seen := byID[intents[i].ID]; !seen {
			ids = append(ids, intents[i].ID)
		}
		byID[intents[i].ID] = &intents[i]
	}
	w := a.Live.DB.Write
	snaps, err := mailops.Snapshots(ctx, w, ids)
	if err != nil {
		return err
	}
	items, skipped, err := mailops.Verify(ctx, w, client, account, snaps)
	if err != nil {
		return err
	}
	for _, s := range skipped {
		in := byID[s.ID]
		if err := a.Live.Skip(ctx, account, in.ID, in.Digest, in.Kind, nil, in.Dest, s.Reason, in.DecisionID); err != nil {
			return err
		}
		out.Skipped++
	}
	protected, err := protectedNames(ctx, client)
	if err != nil {
		return err
	}
	folders := map[string]string{}
	if account != config.Gmail {
		boxes, err := client.ListMailboxes(ctx)
		if err != nil {
			return err
		}
		for _, info := range boxes {
			folders[info.Name] = info.IMAPName
		}
	}
	trash := ""
	var moves, labels, trashes, flagging groups
	for _, item := range items {
		in := byID[item.ID]
		reason := in.check(account, item)
		if reason == "" && protected[item.IMAPName] {
			reason = "protected mailbox"
		}
		destIMAP := ""
		added, removed := []string{}, []string{}
		if reason == "" && (in.Kind == live.File || in.Kind == live.Correct) {
			dest := ""
			if in.Dest != nil {
				dest = *in.Dest
			}
			switch {
			case !safeLabel(dest):
				reason = "unsafe destination"
			case account == config.Gmail:
				if !slices.Contains(item.Labels, dest) {
					added = []string{dest}
				}
				for _, l := range append(slices.Clone(in.Remove), mailops.InboxLabel) {
					if slices.Contains(item.Labels, l) && l != dest {
						removed = append(removed, l)
					}
				}
				if len(added) == 0 && len(removed) == 0 {
					reason = alreadyThere
				}
			default:
				d, ok := folders[dest]
				switch {
				case !ok || protected[d]:
					reason = "no such folder"
				case d == item.IMAPName:
					reason = alreadyThere
				default:
					destIMAP = d
				}
			}
		}
		if reason == "" && in.Kind == live.Trash && trash == "" {
			t, err := mailops.TrashMailbox(ctx, client, account)
			if opErr, ok := errors.AsType[mailops.Error](err); ok {
				reason = opErr.Error()
			} else if err != nil {
				return err
			}
			trash = t
		}
		if reason != "" {
			if reason != alreadyThere {
				if err := a.Live.Skip(ctx, account, in.ID, in.Digest, in.Kind, &item.IMAPName, in.Dest, reason, in.DecisionID); err != nil {
					return err
				}
				out.Skipped++
			}
			continue
		}
		dest := in.Dest
		if in.Kind == live.Trash {
			dest = &trash
		}
		aid, ok, err := a.Live.Begin(ctx, account, item.ID, in.Digest, in.Kind, &item.IMAPName, dest, beforeOf(item), in.DecisionID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		e := entry{item: item, intent: in, aid: aid}
		box := func(run func(context.Context, mail.Client, string, *group, *Outcome) error, dest string) func() *group {
			return func() *group {
				return &group{run: run, imapName: item.IMAPName, validity: item.UIDValidity, dest: dest, added: added, removed: removed}
			}
		}
		switch {
		case in.Kind == live.Flag:
			flagging.add(boxKey(item.IMAPName, item.UIDValidity), box(a.flagGroup, ""), e)
		case in.Kind == live.Trash && account == config.Gmail:
			trashes.add(boxKey(item.IMAPName, item.UIDValidity, trash), box(a.gmailTrashGroup, trash), e)
		case in.Kind == live.Trash:
			moves.add(boxKey(item.IMAPName, item.UIDValidity, trash), box(a.moveGroup, trash), e)
		case account == config.Gmail:
			labels.add(boxKey(item.IMAPName, item.UIDValidity, added, removed), box(a.labelGroup, ""), e)
		default:
			moves.add(boxKey(item.IMAPName, item.UIDValidity, destIMAP), box(a.moveGroup, destIMAP), e)
		}
	}
	for _, g := range slices.Concat(moves.order, labels.order, trashes.order, flagging.order) {
		runErr := g.run(ctx, client, account, g, out)
		if runErr == nil {
			continue
		}
		log.Error(account+": group failed unexpectedly", logx.Err, runErr.Error())
		_, refused := errors.AsType[safety.WriteRefused](runErr)
		text := py.ExceptionText(runErr)
		for _, e := range g.entries {
			var err error
			if refused {
				err = a.Live.Discard(ctx, e.aid)
			} else {
				err = a.Live.Hold(ctx, e.aid, text, nil)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *Applier) inboxIntents(ctx context.Context, account string, budget *Budget) ([]Intent, error) {
	rows, err := a.query(ctx, account, inboxCandidates)
	if err != nil {
		return nil, err
	}
	found, err := db.Collect(rows, func(r *sql.Rows) (Intent, error) {
		var in Intent
		var decisionID int64
		var action string
		err := r.Scan(&in.ID, &in.Digest, &decisionID, &action, &in.Dest)
		in.DecisionID = &decisionID
		in.check = readInbox
		if action == rules.ActionDelete {
			in.Kind, in.Dest = live.Trash, nil
		} else {
			in.Kind = live.File
		}
		return in, err
	})
	if err != nil {
		return nil, err
	}
	return take(budget, found, "inbox decisions"), nil
}

func (a *Applier) correctionIntents(ctx context.Context, account string, budget *Budget) ([]Intent, error) {
	rows, err := a.query(ctx, account, correctionCandidates)
	if err != nil {
		return nil, err
	}
	type row struct {
		id             int64
		digest, target string
		location       sql.NullString
		labels         sql.NullString
	}
	all, err := db.Collect(rows, func(r *sql.Rows) (row, error) {
		var x row
		err := r.Scan(&x.id, &x.digest, &x.location, &x.labels, &x.target)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	var pending []row
	for _, x := range all {
		var atTarget bool
		if account == config.Gmail {
			var labels []string
			if err := json.Unmarshal([]byte(x.labels.String), &labels); err != nil {
				return nil, err
			}
			atTarget = slices.Contains(mail.CollapseLabels(labels), x.target)
		} else {
			atTarget = x.location.Valid && x.location.String == x.target
		}
		if !atTarget {
			pending = append(pending, x)
		}
	}
	var out []Intent
	for _, x := range take(budget, pending, "corrections") {
		var remove []string
		if account == config.Gmail {
			var prior sql.NullString
			err := a.Live.DB.Write.QueryRowContext(ctx, priorLabel, account, x.digest).Scan(&prior)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if prior.String != "" {
				remove = []string{prior.String}
			}
		}
		out = append(out, Intent{ID: x.id, Digest: x.digest, Kind: live.Correct, check: anywhere, Dest: new(x.target), Remove: remove})
	}
	return out, nil
}

func (a *Applier) flagIntents(ctx context.Context, account string, budget *Budget) ([]Intent, error) {
	rows, err := a.query(ctx, account, flagCandidates)
	if err != nil {
		return nil, err
	}
	found, err := db.Collect(rows, func(r *sql.Rows) (Intent, error) {
		in := Intent{Kind: live.Flag, check: unreadInbox}
		err := r.Scan(&in.ID, &in.Digest)
		return in, err
	})
	if err != nil {
		return nil, err
	}
	return take(budget, found, "flags"), nil
}

func hasAge(rule *rules.Rule) bool {
	return slices.ContainsFunc(rule.Conditions, func(c rules.Condition) bool { return c.Field == rules.AgeField })
}

type withExtra struct {
	rows   *sql.Rows
	seen   *bool
	digest *string
}

func (s withExtra) Scan(dest ...any) error { return s.rows.Scan(append(dest, s.seen, s.digest)...) }

func (a *Applier) deletionIntents(ctx context.Context, account string, budget *Budget) ([]Intent, error) {
	ruleset, err := rules.LoadRules(ctx, a.Live.DB.Write, account)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(ruleset, func(r rules.Rule) bool { return r.Action == rules.ActionDelete }) {
		return nil, nil
	}
	rows, err := a.query(ctx, account, deletionCandidates)
	if err != nil {
		return nil, err
	}
	type row struct {
		msg    rules.Message
		seen   bool
		digest string
	}
	all, err := db.Collect(rows, func(r *sql.Rows) (row, error) {
		var x row
		m, err := rules.ScanMessage(withExtra{r, &x.seen, &x.digest})
		x.msg = m
		return x, err
	})
	if err != nil {
		return nil, err
	}
	type match struct {
		row  row
		rule *rules.Rule
	}
	var matched []match
	for _, x := range all {
		rule, err := rules.FirstMatch(&x.msg, ruleset, a.now())
		if err != nil {
			return nil, err
		}
		if rule != nil && rule.Action == rules.ActionDelete && (x.seen || hasAge(rule)) {
			matched = append(matched, match{x, rule})
		}
	}
	var out []Intent
	for _, m := range take(budget, matched, "deletions") {
		c := check(unflaggedRead)
		if hasAge(m.rule) {
			c = unflagged
		}
		out = append(out, Intent{ID: m.row.msg.ID, Digest: m.row.digest, Kind: live.Trash, check: c})
	}
	return out, nil
}

func (a *Applier) deletionDue(ctx context.Context, account string) (bool, error) {
	last, err := live.GetState(ctx, a.Live.DB.Write, live.DeletionKey+":"+account)
	if err != nil || last == nil {
		return err == nil, err
	}
	ts, err := strconv.ParseFloat(py.Strip(*last), 64)
	if err != nil {
		return false, err
	}
	return a.now()-ts >= config.DeletionIntervalSeconds, nil
}

type builder func(ctx context.Context, budget *Budget) ([]Intent, error)

func (a *Applier) runPass(ctx context.Context, account string, build builder, label string) (Outcome, error) {
	var out Outcome
	if a.Live.Cfg.Mode != config.Live {
		return out, nil
	}
	pause := a.Live.Cfg.PausePath()
	if safety.IsPaused(pause) {
		log.Info(account + ": paused, automatic writes skipped")
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	out.Ran = true
	ctx = context.WithoutCancel(ctx)
	budget := &Budget{Left: a.cap()}
	auto := safety.AutomaticWrites(ctx, pause)
	if _, err := a.Live.Recover(auto); err != nil {
		return out, err
	}
	intents, err := build(auto, budget)
	if err != nil {
		return out, err
	}
	if len(intents) > 0 {
		err := mailops.Session(auto, a.Open, account, func(client mail.Client) error {
			return a.execute(auto, client, account, intents, &out)
		})
		if err != nil {
			return out, err
		}
	}
	if out.Written > 0 {
		if err := a.Resync(ctx, account); err != nil {
			return out, err
		}
	}
	if out.Written > 0 || out.Skipped > 0 || out.Failed > 0 || budget.Deferred > 0 {
		byKind := out.ByKind
		if byKind == nil {
			byKind = py.Dict{}
		}
		log.Info(fmt.Sprintf("%s: %s applied=%s skipped=%d failed=%d deferred=%d",
			account, label, py.ReprValue(byKind), out.Skipped, out.Failed, budget.Deferred))
	}
	return out, nil
}

func (a *Applier) ApplyAccount(ctx context.Context, account string) (Outcome, error) {
	return a.runPass(ctx, account, func(ctx context.Context, budget *Budget) ([]Intent, error) {
		var out []Intent
		for _, build := range [...]func(context.Context, string, *Budget) ([]Intent, error){a.correctionIntents, a.inboxIntents, a.flagIntents} {
			intents, err := build(ctx, account, budget)
			if err != nil {
				return nil, err
			}
			out = append(out, intents...)
		}
		return out, nil
	}, "live pass")
}

func (a *Applier) ApplyCorrections(ctx context.Context, account string, messageIDs []int64) (Outcome, error) {
	return a.runPass(ctx, account, func(ctx context.Context, budget *Budget) ([]Intent, error) {
		intents, err := a.correctionIntents(ctx, account, &Budget{Left: budget.Left})
		if err != nil {
			return nil, err
		}
		return slices.DeleteFunc(intents, func(in Intent) bool { return !slices.Contains(messageIDs, in.ID) }), nil
	}, "corrections")
}

func (a *Applier) DeletionPass(ctx context.Context, account string, force bool) (Outcome, error) {
	if !force {
		due, err := a.deletionDue(ctx, account)
		if err != nil || !due {
			return Outcome{}, err
		}
	}
	out, err := a.runPass(ctx, account, func(ctx context.Context, budget *Budget) ([]Intent, error) {
		return a.deletionIntents(ctx, account, budget)
	}, "deletion pass")
	if err != nil || !out.Ran {
		return out, err
	}
	return out, live.SetState(context.WithoutCancel(ctx), a.Live.DB.Write, live.DeletionKey+":"+account, py.FloatRepr(a.now()))
}

func (a *Applier) ApplyAll(ctx context.Context, account string) (applied, deleted Outcome, err error) {
	if applied, err = a.ApplyAccount(ctx, account); err != nil {
		return applied, deleted, err
	}
	deleted, err = a.DeletionPass(ctx, account, false)
	return applied, deleted, err
}
