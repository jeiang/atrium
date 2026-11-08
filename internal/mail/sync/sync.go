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
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/features"
	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/py"
)

type limits struct {
	fetchBatch, flagBatch, sentBackfillDays, ftsBodyChars int
}

func readLimits(ctx context.Context, q db.Querier) (limits, error) {
	r := endpoint.Read(ctx, q)
	l := limits{fetchBatch: r.Int(mail.KeyFetchBatch), flagBatch: r.Int(mail.KeyFlagBatch), sentBackfillDays: r.Int(mail.KeySentBackfillDays), ftsBodyChars: r.Int(mail.KeyFTSBodyChars)}
	return l, r.Err()
}

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

func (s *Syncer) One(ctx context.Context, account string, open mail.OpenFunc) (Stats, error) {
	client, err := open(ctx, mail.OpenOptions{Account: account, Q: s.DB.Read})
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

type checkpoint struct {
	Valid      bool
	Validity   int64
	HighestUID uint32
	ModSeq     uint64
}

type mailbox struct {
	ID         int64
	Name       string
	IMAPName   string
	Role       string
	Checkpoint checkpoint
}

func (s *Syncer) Account(ctx context.Context, account string, client mail.Client) (Stats, error) {
	var stats Stats
	if _, err := s.DB.Write.ExecContext(ctx, "INSERT OR IGNORE INTO accounts (name, kind) VALUES (?, ?)", account, config.From(ctx).Preset(account)); err != nil {
		return stats, err
	}
	lim, err := readLimits(ctx, s.DB.Read)
	if err != nil {
		return stats, err
	}
	run := s.now()
	mailboxes, err := s.upsertMailboxes(ctx, account, client, run, &stats)
	if err != nil {
		return stats, err
	}
	for _, mbx := range mailboxes {
		if err := s.syncMailbox(ctx, client, account, mbx, run, lim, &stats); err != nil {
			return stats, err
		}
		stats.Mailboxes++
	}
	if err := s.reconcile(ctx, account, stats.GoneIDs, run); err != nil {
		return stats, err
	}
	_, err = s.DB.Write.ExecContext(ctx, "UPDATE accounts SET last_sync_at = ? WHERE name = ?", run, account)
	return stats, err
}

func nullableModSeq(v uint64) any {
	if v == 0 {
		return nil
	}
	return int64(v)
}

func (s *Syncer) retireMailboxes(ctx context.Context, account string, listed map[string]bool, run float64, stats *Stats) error {
	rows, err := s.DB.Read.QueryContext(ctx, "SELECT id, name FROM mailboxes WHERE account = ?", account)
	if err != nil {
		return err
	}
	existing, err := db.Collect(rows, func(r *sql.Rows) (mailbox, error) {
		var m mailbox
		err := r.Scan(&m.ID, &m.Name)
		return m, err
	})
	if err != nil {
		return err
	}
	for _, mb := range existing {
		if listed[mb.Name] {
			continue
		}
		rows, err := s.DB.Read.QueryContext(ctx, "SELECT id FROM messages WHERE mailbox_id = ? AND gone_at IS NULL", mb.ID)
		if err != nil {
			return err
		}
		ids, err := db.Collect(rows, func(r *sql.Rows) (int64, error) {
			var id int64
			return id, r.Scan(&id)
		})
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			if _, err := s.DB.Write.ExecContext(ctx, "UPDATE messages SET gone_at = ? WHERE mailbox_id = ? AND gone_at IS NULL", run, mb.ID); err != nil {
				return err
			}
			stats.GoneIDs = append(stats.GoneIDs, ids...)
		}
		var remaining int
		if err := s.DB.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE mailbox_id = ?", mb.ID).Scan(&remaining); err != nil {
			return err
		}
		if remaining == 0 {
			if _, err := s.DB.Write.ExecContext(ctx, "DELETE FROM mailboxes WHERE id = ?", mb.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Syncer) upsertMailboxes(ctx context.Context, account string, client mail.Client, run float64, stats *Stats) ([]mailbox, error) {
	listed, err := client.ListMailboxes(ctx)
	if err != nil {
		return nil, err
	}
	listedNames := map[string]bool{}
	for _, info := range listed {
		listedNames[info.Name] = true
	}
	if err := s.retireMailboxes(ctx, account, listedNames, run, stats); err != nil {
		return nil, err
	}
	type chosen struct {
		info mail.MailboxInfo
		role string
	}
	var picked []chosen
	for _, info := range listed {
		role := mail.MailboxRole(info.Name, info.Flags, config.From(ctx).Preset(account))
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
		var validity, highestUID, modseq sql.NullInt64
		if err := rows.Scan(&m.ID, &m.Name, &m.IMAPName, &m.Role, &validity, &highestUID, &modseq); err != nil {
			return nil, err
		}
		if validity.Valid && highestUID.Valid {
			m.Checkpoint = checkpoint{Valid: true, Validity: validity.Int64, HighestUID: uint32(highestUID.Int64)}
			if modseq.Valid {
				m.Checkpoint.ModSeq = uint64(modseq.Int64)
			}
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

func (s *Syncer) syncMailbox(ctx context.Context, client mail.Client, account string, mbx mailbox, run float64, lim limits, stats *Stats) error {
	gmail := config.From(ctx).Preset(account) == config.Gmail
	status, err := client.Status(ctx, mbx.IMAPName)
	if err != nil {
		return err
	}
	validity := int64(status.UIDValidity)
	current := checkpoint{Valid: true, Validity: validity}
	if status.HighestModSeq != nil {
		current.ModSeq = uint64(*status.HighestModSeq)
	}
	prev := mbx.Checkpoint
	if prev.Valid && prev.Validity != validity {
		if _, err := s.DB.Write.ExecContext(ctx, "UPDATE messages SET gone_at = ? WHERE mailbox_id = ? AND gone_at IS NULL", run, mbx.ID); err != nil {
			return err
		}
		if _, err := s.DB.Write.ExecContext(ctx,
			"UPDATE mailboxes SET uidvalidity = NULL, highest_uid = 0, highestmodseq = NULL WHERE id = ?", mbx.ID); err != nil {
			return err
		}
		prev = checkpoint{}
	}
	if err := client.Select(ctx, mbx.IMAPName); err != nil {
		return err
	}
	var serverUIDs []uint32
	if mbx.Role == "sent" {
		since, err := s.SentSince(ctx, mbx.ID, validity, run, lim.sentBackfillDays)
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
	if mbx.Role != "sent" {
		for _, u := range liveOrder {
			if !present[u] {
				vanished = append(vanished, live[u])
			}
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
	if err := s.refreshFlags(ctx, client, account, mbx, survivors, prev, current, lim, stats, run); err != nil {
		return err
	}
	var fresh []uint32
	for u := range present {
		if !known[u] {
			fresh = append(fresh, u)
		}
	}
	slices.Sort(fresh)
	for start := 0; start < len(fresh); start += lim.fetchBatch {
		batch := fresh[start:min(start+lim.fetchBatch, len(fresh))]
		fetched, err := client.UIDFetch(ctx, mail.UIDs(batch...), mail.FetchRequest{Mode: mail.FetchFull, Labels: gmail})
		if err != nil {
			return err
		}
		err = s.DB.WithTx(ctx, func(tx *sql.Tx) error {
			for _, item := range fetched {
				if err := s.ingest(ctx, tx, account, mbx, validity, lim.ftsBodyChars, run, item); err != nil {
					return err
				}
				stats.New++
			}
			return nil
		})
		if err != nil {
			return err
		}
		log.Info(fmt.Sprintf("%s/%s fetched %d/%d", account, mbx.Name, min(start+lim.fetchBatch, len(fresh)), len(fresh)))
	}
	if gmail {
		if err := s.refreshCategories(ctx, client, mbx); err != nil {
			return err
		}
	}
	var top uint32
	for _, u := range serverUIDs {
		top = max(top, u)
	}
	if prev.Valid && prev.Validity == validity {
		top = max(top, prev.HighestUID)
	}
	_, err = s.DB.Write.ExecContext(ctx,
		"UPDATE mailboxes SET uidvalidity = ?, highest_uid = ?, highestmodseq = ?, synced_at = ? WHERE id = ?",
		validity, top, nullableModSeq(current.ModSeq), run, mbx.ID)
	if err != nil {
		return err
	}
	return nil
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

func (s *Syncer) SentSince(ctx context.Context, mailboxID, validity int64, run float64, backfillDays int) (time.Time, error) {
	cutoff := run - float64(backfillDays)*86400
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
	survivors map[uint32]stored, prev, current checkpoint, lim limits, stats *Stats, run float64) error {
	if len(survivors) == 0 {
		return nil
	}
	gmail := config.From(ctx).Preset(account) == config.Gmail
	items := mail.FetchRequest{Mode: mail.FetchFlags, Labels: gmail}
	var updates []mail.Message
	if client.HasCapability("CONDSTORE") && current.ModSeq != 0 && prev.Valid && prev.ModSeq != 0 {
		if current.ModSeq == prev.ModSeq {
			return nil
		}
		items.ChangedSince = prev.ModSeq
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
		for start := 0; start < len(uids); start += lim.flagBatch {
			got, err := client.UIDFetch(ctx, mail.UIDs(uids[start:min(start+lim.flagBatch, len(uids))]...), items)
			if err != nil {
				return err
			}
			updates = append(updates, got...)
		}
	}
	ts := run
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
		state := MessageState(config.From(ctx).Preset(account), mbx.Role, mbx.Name, item.Flags, labels)
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
			if err != nil {
				return err
			}
			return nil
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
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
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

func (s *Syncer) ingest(ctx context.Context, tx *sql.Tx, account string, mbx mailbox, validity int64, ftsBodyChars int, run float64, item mail.Message) error {
	var attachments []features.Attachment
	if item.Attachments != nil {
		attachments = make([]features.Attachment, len(item.Attachments))
		for i, a := range item.Attachments {
			attachments[i] = features.Attachment{Type: a.Kind, Name: a.Name}
		}
	}
	feats := features.Extract(item.Header, item.Text, attachments, internalTS(item.InternalDate), ftsBodyChars)
	digest := features.ContentDigest(item.Header, item.Text)
	relative, absolute := RawLocation(s.Cfg, account, mbx.ID, validity, item.UID)
	raw := make([]byte, 0, len(item.Header)+len(item.Text))
	raw = append(append(raw, item.Header...), item.Text...)
	if err := storeRaw(absolute, raw); err != nil {
		return err
	}
	state := MessageState(config.From(ctx).Preset(account), mbx.Role, mbx.Name, item.Flags, item.Labels)
	id, err := rules.InsertMessage(ctx, tx, rules.MessageInsert{
		Account: account, MailboxID: mbx.ID, UIDValidity: validity, UID: item.UID, MsgID: feats.MsgID, Digest: digest,
		GmMsgID: item.GmMsgID, GmThrID: item.GmThrID, InternalDate: item.InternalDate, DateTS: feats.DateTS,
		Size: item.Size, RawPath: relative, Flags: state.Flags, Seen: state.Seen, Flagged: state.Flagged, Labels: state.Labels,
		InInbox: state.InInbox, Sent: state.Sent, Location: state.Location, Folder: state.Folder,
		FromAddr: feats.FromAddr, FromDomain: feats.FromDomain, FromName: feats.FromName, ToAddrs: py.JSONDumps(nonNil(feats.ToAddrs)),
		ReplyTo: feats.ReplyTo, RPathDomain: feats.RPathDomain, ListID: feats.ListID, Precedence: feats.Precedence,
		Subject: feats.Subject, SubjectTmpl: feats.SubjectTmpl, MsgIDDomain: feats.MsgIDDomain, InReplyTo: feats.InReplyTo,
		Refs: feats.Refs, DKIMD: py.JSONDumps(nonNil(feats.DKIMD)), ESP: py.JSONDumps(nonNil(feats.ESP)),
		AttTypes: py.JSONDumps(nonNil(feats.AttTypes)), AttNames: py.JSONDumps(nonNil(feats.AttNames)),
		KOrder: feats.KOrder, KVCode: feats.KVCode, KOTP6: feats.KOTP6, FirstSeen: run,
	})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO message_fts (rowid, subject, sender, body) VALUES (?, ?, ?, ?)",
		id, feats.Subject, feats.FromName+" <"+feats.FromAddr+">", feats.Body)
	if err != nil {
		return err
	}
	return storeRaw(absolute, raw)
}

func (s *Syncer) reconcile(ctx context.Context, account string, goneIDs []int64, run float64) error {
	ts := run
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
