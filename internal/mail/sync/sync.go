package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/features"
	"github.com/jeiang/atrium/internal/py"
)

const (
	SentBackfillDays = 90
	FlagBatch        = 500
)

var (
	log    = logx.Logger("atrium.sync")
	runLog = logx.Logger("atrium.run")
)

type Stats struct {
	New         int
	GoneIDs     []int64
	FlagChanges int
	Mailboxes   int
}

type Syncer struct {
	DB  *db.DB
	Cfg config.Config
	Now func() float64
}

func (s *Syncer) now() float64 {
	if s.Now != nil {
		return s.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

type Opener func(ctx context.Context, account string) (mail.Client, error)

func (s *Syncer) One(ctx context.Context, account string, open Opener) (Stats, error) {
	client, err := open(ctx, account)
	if err != nil {
		return Stats{}, err
	}
	defer client.Close()
	stats, err := s.Account(ctx, account, client)
	if err != nil {
		return stats, err
	}
	runLog.Info(fmt.Sprintf("%s: %d new, %d gone, %d flag changes", account, stats.New, len(stats.GoneIDs), stats.FlagChanges))
	return stats, nil
}

type mailbox struct {
	ID            int64
	Name          string
	IMAPName      string
	Role          string
	UIDValidity   *int64
	HighestUID    int64
	HighestModSeq *int64
}

func (s *Syncer) Account(ctx context.Context, account string, client mail.Client) (Stats, error) {
	var stats Stats
	if _, err := s.DB.Write.ExecContext(ctx, "INSERT OR IGNORE INTO accounts (name, kind) VALUES (?, ?)", account, account); err != nil {
		return stats, err
	}
	run := s.now()
	mailboxes, err := s.upsertMailboxes(ctx, account, client)
	if err != nil {
		return stats, err
	}
	for _, mbx := range mailboxes {
		if err := s.syncMailbox(ctx, client, account, mbx, run, &stats); err != nil {
			return stats, err
		}
		stats.Mailboxes++
	}
	if err := s.reconcile(ctx, account, stats.GoneIDs); err != nil {
		return stats, err
	}
	_, err = s.DB.Write.ExecContext(ctx, "UPDATE accounts SET last_sync_at = ? WHERE name = ?", run, account)
	return stats, err
}

func (s *Syncer) upsertMailboxes(ctx context.Context, account string, client mail.Client) ([]mailbox, error) {
	listed, err := client.ListMailboxes(ctx)
	if err != nil {
		return nil, err
	}
	type chosen struct {
		info mail.MailboxInfo
		role string
	}
	var picked []chosen
	for _, info := range listed {
		role := mail.MailboxRole(info.Name, info.Flags, account)
		if (systemRoles[role] && !indexedSystemRoles[role]) || role == "system" {
			continue
		}
		if slices.ContainsFunc(info.Flags, func(f string) bool { return strings.ToUpper(f) == `\NOSELECT` }) {
			continue
		}
		picked = append(picked, chosen{info, role})
	}
	for _, c := range picked {
		if _, err := s.DB.Write.ExecContext(ctx, `
			INSERT INTO mailboxes (account, name, imap_name, role) VALUES (?, ?, ?, ?)
			ON CONFLICT (account, name) DO UPDATE SET imap_name = excluded.imap_name, role = excluded.role
			`, account, c.info.Name, c.info.IMAPName, c.role); err != nil {
			return nil, err
		}
	}
	if len(picked) == 0 {
		return nil, nil
	}
	args := []any{account}
	for _, c := range picked {
		args = append(args, c.info.Name)
	}
	rows, err := s.DB.Read.QueryContext(ctx,
		"SELECT id, name, imap_name, role, uidvalidity, highest_uid, highestmodseq FROM mailboxes WHERE account = ? AND name IN ("+
			strings.TrimSuffix(strings.Repeat("?,", len(picked)), ",")+")", args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []mailbox
	for rows.Next() {
		var m mailbox
		if err := rows.Scan(&m.ID, &m.Name, &m.IMAPName, &m.Role, &m.UIDValidity, &m.HighestUID, &m.HighestModSeq); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortStableFunc(out, func(a, b mailbox) int {
		if x, y := a.Role == "inbox", b.Role == "inbox"; x != y {
			if y {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}

type stored struct {
	ID       int64
	UID      uint32
	GoneAt   *float64
	Flags    string
	Labels   string
	Location string
	InInbox  int
}

func (s *Syncer) syncMailbox(ctx context.Context, client mail.Client, account string, mbx mailbox, run float64, stats *Stats) error {
	gmail := account == config.Gmail
	status, err := client.Status(ctx, mbx.IMAPName)
	if err != nil {
		return err
	}
	validity := int64(status.UIDValidity)
	var modseq *int64
	if status.HighestModSeq != nil {
		v := int64(*status.HighestModSeq)
		modseq = &v
	}
	storedModseq := mbx.HighestModSeq
	if mbx.UIDValidity != nil && *mbx.UIDValidity != validity {
		if _, err := s.DB.Write.ExecContext(ctx, "UPDATE messages SET gone_at = ? WHERE mailbox_id = ? AND gone_at IS NULL", run, mbx.ID); err != nil {
			return err
		}
		if _, err := s.DB.Write.ExecContext(ctx,
			"UPDATE mailboxes SET uidvalidity = NULL, highest_uid = 0, highestmodseq = NULL WHERE id = ?", mbx.ID); err != nil {
			return err
		}
		storedModseq = nil
	}
	if err := client.Select(ctx, mbx.IMAPName); err != nil {
		return err
	}
	var serverUIDs []uint32
	if mbx.Role == "sent" {
		since, err := s.SentSince(ctx, mbx.ID, validity, run)
		if err != nil {
			return err
		}
		serverUIDs, err = client.UIDSearch(ctx, mail.SearchCriteria{Since: since})
		if err != nil {
			return err
		}
	} else if serverUIDs, err = client.UIDSearch(ctx, mail.SearchCriteria{}); err != nil {
		return err
	}
	rows, err := s.storedRows(ctx, mbx.ID, validity)
	if err != nil {
		return err
	}
	live := map[uint32]stored{}
	known := map[uint32]bool{}
	var liveOrder []uint32
	for _, r := range rows {
		known[r.UID] = true
		if r.GoneAt == nil {
			live[r.UID] = r
			liveOrder = append(liveOrder, r.UID)
		}
	}
	present := map[uint32]bool{}
	for _, u := range serverUIDs {
		present[u] = true
	}
	var vanished []stored
	for _, u := range liveOrder {
		if !present[u] {
			vanished = append(vanished, live[u])
		}
	}
	if len(vanished) > 0 {
		err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
			for _, r := range vanished {
				if _, err := tx.ExecContext(ctx, "UPDATE messages SET gone_at = ? WHERE id = ?", run, r.ID); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, r := range vanished {
			stats.GoneIDs = append(stats.GoneIDs, r.ID)
		}
	}
	survivors := map[uint32]stored{}
	for u, r := range live {
		if present[u] {
			survivors[u] = r
		}
	}
	if err := s.refreshFlags(ctx, client, account, mbx, survivors, storedModseq, modseq, stats); err != nil {
		return err
	}
	var fresh []uint32
	for u := range present {
		if !known[u] {
			fresh = append(fresh, u)
		}
	}
	slices.Sort(fresh)
	for start := 0; start < len(fresh); start += config.FetchBatch {
		batch := fresh[start:min(start+config.FetchBatch, len(fresh))]
		fetched, err := client.UIDFetch(ctx, mail.UIDs(batch...), mail.FetchItems{Labels: gmail, Header: true, Body: true})
		if err != nil {
			return err
		}
		err = s.DB.WithTx(ctx, func(tx *sql.Tx) error {
			for _, item := range fetched {
				if err := s.ingest(ctx, tx, account, mbx, validity, item); err != nil {
					return err
				}
				stats.New++
			}
			return nil
		})
		if err != nil {
			return err
		}
		log.Info(fmt.Sprintf("%s/%s fetched %d/%d", account, mbx.Name, min(start+config.FetchBatch, len(fresh)), len(fresh)))
	}
	if gmail {
		if err := s.refreshCategories(ctx, client, mbx); err != nil {
			return err
		}
	}
	var top int64
	for _, u := range serverUIDs {
		top = max(top, int64(u))
	}
	if mbx.UIDValidity != nil && *mbx.UIDValidity == validity {
		top = max(top, mbx.HighestUID)
	}
	_, err = s.DB.Write.ExecContext(ctx,
		"UPDATE mailboxes SET uidvalidity = ?, highest_uid = ?, highestmodseq = ?, synced_at = ? WHERE id = ?",
		validity, top, modseq, run, mbx.ID)
	return err
}

func (s *Syncer) storedRows(ctx context.Context, mailboxID, validity int64) ([]stored, error) {
	rows, err := s.DB.Read.QueryContext(ctx,
		"SELECT id, uid, gone_at, flags, labels, location, in_inbox FROM messages WHERE mailbox_id = ? AND uidvalidity = ?",
		mailboxID, validity)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []stored
	for rows.Next() {
		var r stored
		if err := rows.Scan(&r.ID, &r.UID, &r.GoneAt, &r.Flags, &r.Labels, &r.Location, &r.InInbox); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func dayOf(ts float64) time.Time {
	t := time.Unix(int64(math.Floor(ts)), 0).UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func IMAPDate(t time.Time) string { return t.UTC().Format("02-Jan-2006") }

func (s *Syncer) SentSince(ctx context.Context, mailboxID, validity int64, run float64) (time.Time, error) {
	cutoff := run - SentBackfillDays*86400
	var oldest sql.NullFloat64
	err := s.DB.Read.QueryRowContext(ctx,
		"SELECT MIN(date_ts) FROM messages WHERE mailbox_id = ? AND uidvalidity = ? AND gone_at IS NULL AND date_ts > 0",
		mailboxID, validity).Scan(&oldest)
	if err != nil {
		return time.Time{}, err
	}
	if oldest.Valid {
		cutoff = min(cutoff, oldest.Float64-2*86400)
	}
	return dayOf(cutoff), nil
}

func (s *Syncer) refreshFlags(ctx context.Context, client mail.Client, account string, mbx mailbox,
	survivors map[uint32]stored, storedModseq, modseq *int64, stats *Stats) error {
	if len(survivors) == 0 {
		return nil
	}
	gmail := account == config.Gmail
	items := mail.FetchItems{Labels: gmail}
	var updates []mail.Message
	if client.HasCapability("CONDSTORE") && modseq != nil && storedModseq != nil {
		if *modseq == *storedModseq {
			return nil
		}
		items.ChangedSince = uint64(*storedModseq)
		var err error
		if updates, err = client.UIDFetch(ctx, mail.AllUIDs(), items); err != nil {
			return err
		}
	} else {
		uids := make([]uint32, 0, len(survivors))
		for u := range survivors {
			uids = append(uids, u)
		}
		slices.Sort(uids)
		for start := 0; start < len(uids); start += FlagBatch {
			got, err := client.UIDFetch(ctx, mail.UIDs(uids[start:min(start+FlagBatch, len(uids))]...), items)
			if err != nil {
				return err
			}
			updates = append(updates, got...)
		}
	}
	ts := s.now()
	for _, item := range updates {
		row, ok := survivors[item.UID]
		if !ok {
			continue
		}
		labels := item.Labels
		if labels == nil {
			if err := json.Unmarshal([]byte(row.Labels), &labels); err != nil {
				return err
			}
		}
		state := MessageState(account, mbx.Role, mbx.Name, item.Flags, labels)
		if state.Flags == row.Flags && state.Labels == row.Labels {
			continue
		}
		var oldFlags, newFlags []string
		if err := json.Unmarshal([]byte(row.Flags), &oldFlags); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(state.Flags), &newFlags); err != nil {
			return err
		}
		detail := py.Dict{
			{Key: "flags", Value: []any{nonNil(oldFlags), nonNil(newFlags)}},
			{Key: "location", Value: []any{row.Location, state.Location}},
			{Key: "in_inbox", Value: []any{row.InInbox, state.InInbox}},
		}
		kind := "flags"
		if state.Labels != row.Labels {
			kind = "labels"
		}
		err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx,
				"UPDATE messages SET seen = ?, flagged = ?, in_inbox = ?, sent = ?, location = ?, folder = ?, flags = ?, labels = ? WHERE id = ?",
				state.Seen, state.Flagged, state.InInbox, state.Sent, state.Location, state.Folder, state.Flags, state.Labels, row.ID); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO events (ts, account, kind, message_id, detail) VALUES (?, ?, ?, ?, ?)",
				ts, account, kind, row.ID, py.JSONDumps(detail))
			return err
		})
		if err != nil {
			return err
		}
		stats.FlagChanges++
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Syncer) refreshCategories(ctx context.Context, client mail.Client, mbx mailbox) error {
	assigned := map[uint32]string{}
	for _, category := range config.GmailCategories {
		uids, err := client.UIDSearch(ctx, mail.SearchCriteria{GmailRaw: "category:" + category})
		if err != nil {
			return err
		}
		for _, u := range uids {
			assigned[u] = category
		}
	}
	return s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		type row struct {
			id       int64
			uid      uint32
			category string
		}
		rows, err := tx.QueryContext(ctx, "SELECT id, uid, category FROM messages WHERE mailbox_id = ? AND gone_at IS NULL", mbx.ID)
		if err != nil {
			return err
		}
		var all []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.uid, &r.category); err != nil {
				_ = rows.Close()
				return err
			}
			all = append(all, r)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, r := range all {
			wanted, ok := assigned[r.uid]
			if !ok {
				wanted = "primary"
			}
			if r.category != wanted {
				if _, err := tx.ExecContext(ctx, "UPDATE messages SET category = ? WHERE id = ?", wanted, r.id); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func RawLocation(cfg config.Config, account string, mailboxID, validity int64, uid uint32) (relative, absolute string) {
	relative = path.Join("raw", account, strconv.FormatInt(mailboxID, 10), strconv.FormatInt(validity, 10)+"_"+strconv.FormatUint(uint64(uid), 10)+".eml")
	return relative, filepath.Join(cfg.CacheDir, filepath.FromSlash(relative))
}

func storeRaw(file string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o777); err != nil {
		return err
	}
	tmp := file + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(payload); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func internalTS(value string) float64 {
	if ts, ok := features.EmailTS(value); ok {
		return ts
	}
	return 0
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Syncer) ingest(ctx context.Context, tx *sql.Tx, account string, mbx mailbox, validity int64, item mail.Message) error {
	var attachments []features.Attachment
	if item.Attachments != nil {
		attachments = make([]features.Attachment, len(item.Attachments))
		for i, a := range item.Attachments {
			attachments[i] = features.Attachment{Type: a.Kind, Name: a.Name}
		}
	}
	feats, err := features.Extract(item.Header, item.Text, attachments, internalTS(item.InternalDate))
	if err != nil {
		return err
	}
	digest := features.ContentDigest(item.Header, item.Text)
	relative, absolute := RawLocation(s.Cfg, account, mbx.ID, validity, item.UID)
	raw := make([]byte, 0, len(item.Header)+len(item.Text))
	raw = append(append(raw, item.Header...), item.Text...)
	if err := storeRaw(absolute, raw); err != nil {
		return err
	}
	state := MessageState(account, mbx.Role, mbx.Name, item.Flags, item.Labels)
	res, err := tx.ExecContext(ctx, `
		INSERT INTO messages (
			account, mailbox_id, uidvalidity, uid, msgid, digest, gm_msgid, gm_thrid, internaldate, date_ts,
			size, raw_path, flags, seen, flagged, labels, in_inbox, sent, location, folder,
			from_addr, from_domain, from_name, to_addrs, reply_to, rpath_domain, list_id, precedence,
			subject, subject_tmpl, msgid_domain, in_reply_to, refs, dkim_d, esp, att_types, att_names,
			has_money, k_statement, k_receipt, k_order, k_vcode, k_otp6, body_len, first_seen
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		account, mbx.ID, validity, item.UID, feats.MsgID, digest, nullable(item.GmMsgID), nullable(item.GmThrID), item.InternalDate, feats.DateTS,
		item.Size, relative, state.Flags, state.Seen, state.Flagged, state.Labels, state.InInbox, state.Sent, state.Location, state.Folder,
		feats.FromAddr, feats.FromDomain, feats.FromName, py.JSONDumps(nonNil(feats.ToAddrs)), feats.ReplyTo, feats.RPathDomain, feats.ListID, feats.Precedence,
		feats.Subject, feats.SubjectTmpl, feats.MsgIDDomain, feats.InReplyTo, feats.Refs, py.JSONDumps(nonNil(feats.DKIMD)), py.JSONDumps(nonNil(feats.ESP)),
		py.JSONDumps(nonNil(feats.AttTypes)), py.JSONDumps(nonNil(feats.AttNames)),
		bit(feats.HasMoney), bit(feats.KStatement), bit(feats.KReceipt), bit(feats.KOrder), bit(feats.KVCode), bit(feats.KOTP6), feats.BodyLen, s.now())
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO message_fts (rowid, subject, sender, body) VALUES (?, ?, ?, ?)",
		id, feats.Subject, feats.FromName+" <"+feats.FromAddr+">", feats.Body)
	return err
}

func (s *Syncer) reconcile(ctx context.Context, account string, goneIDs []int64) error {
	ts := s.now()
	for _, id := range goneIDs {
		var digest, location string
		var mailboxID int64
		if err := s.DB.Read.QueryRowContext(ctx, "SELECT digest, location, mailbox_id FROM messages WHERE id = ?", id).
			Scan(&digest, &location, &mailboxID); err != nil {
			return err
		}
		var destID int64
		var destLocation string
		err := s.DB.Read.QueryRowContext(ctx,
			"SELECT id, location FROM messages WHERE account = ? AND digest = ? AND gone_at IS NULL AND mailbox_id != ? ORDER BY id DESC LIMIT 1",
			account, digest, mailboxID).Scan(&destID, &destLocation)
		kind := "move"
		var detail py.Dict
		switch {
		case err == nil:
			detail = py.Dict{{Key: "from", Value: location}, {Key: "to", Value: destLocation}, {Key: "to_id", Value: destID}}
		case errors.Is(err, sql.ErrNoRows):
			kind = "delete"
			detail = py.Dict{{Key: "from", Value: location}}
		default:
			return err
		}
		if _, err := s.DB.Write.ExecContext(ctx, "INSERT INTO events (ts, account, kind, message_id, detail) VALUES (?, ?, ?, ?, ?)",
			ts, account, kind, id, py.JSONDumps(detail)); err != nil {
			return err
		}
	}
	return nil
}
