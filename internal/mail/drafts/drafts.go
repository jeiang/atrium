package drafts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"regexp"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mime"
)

type Status string

const (
	Draft     Status = "draft"
	Sending   Status = "sending"
	Sent      Status = "sent"
	Discarded Status = "discarded"
	Unknown   Status = "unknown"
	Invalid   Status = "invalid"

	SendingStaleSeconds = 600
	maxAddresses        = 2000
)

func (s Status) Valid() bool {
	switch s {
	case Draft, Sending, Sent, Discarded, Unknown, Invalid:
		return true
	}
	return false
}

type Stale struct{}

func (Stale) Error() string { return "draft changed, review again" }

var log = logx.Logger("atrium.drafts")

type Error string

func (e Error) Error() string { return string(e) }

type OutcomeUnknown string

func (e OutcomeUnknown) Error() string { return string(e) }

type Row struct {
	ID                 int64    `json:"id"`
	ThreadID           *int64   `json:"thread_id"`
	Account            string   `json:"account"`
	ToAddrs            string   `json:"to_addrs"`
	CcAddrs            string   `json:"cc_addrs"`
	Subject            string   `json:"subject"`
	Body               string   `json:"body"`
	InReplyToMessageID *int64   `json:"in_reply_to_message_id"`
	InReplyTo          string   `json:"in_reply_to"`
	Refs               string   `json:"refs"`
	Status             Status   `json:"status"`
	Revision           int64    `json:"revision"`
	CreatedAt          float64  `json:"created_at"`
	UpdatedAt          float64  `json:"updated_at"`
	SentAt             *float64 `json:"sent_at"`
	SMTPResult         *string  `json:"smtp_result"`
	SentMsgID          *string  `json:"sent_msgid"`
}

type Store struct {
	DB     *db.DB
	Cfg    config.Config
	Ledger *ledger.Store
	Open   mail.OpenFunc
	MsgID  func(domain string) string
	Tx     db.Querier
	Now    func() float64
}

const (
	SendOperation   = "smtp_send"
	AppendOperation = "sent_append"
)

func SendTarget(account string, id int64) string {
	return fmt.Sprintf("mail:%s:draft:%d", account, id)
}

func (s *Store) ledgerStore() *ledger.Store {
	if s.Ledger == nil {
		panic("drafts: the effect ledger store is not configured")
	}
	return s.Ledger
}

func (s *Store) now() float64 {
	if s.Now != nil {
		return s.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (s *Store) read() db.Querier {
	if s.Tx != nil {
		return s.Tx
	}
	return s.DB.Read
}

func (s *Store) write() db.Querier {
	if s.Tx != nil {
		return s.Tx
	}
	return s.DB.Write
}

const columns = "id, thread_id, account, to_addrs, cc_addrs, subject, body, in_reply_to_message_id, in_reply_to, refs, " +
	"status, revision, created_at, updated_at, sent_at, smtp_result, sent_msgid"

type scanner interface{ Scan(dest ...any) error }

func scanRow(sc scanner) (Row, error) {
	var r Row
	var thread, replyTo sql.NullInt64
	var sentAt sql.NullFloat64
	var result, msgid sql.NullString
	var status string
	err := sc.Scan(&r.ID, &thread, &r.Account, &r.ToAddrs, &r.CcAddrs, &r.Subject, &r.Body, &replyTo, &r.InReplyTo, &r.Refs,
		&status, &r.Revision, &r.CreatedAt, &r.UpdatedAt, &sentAt, &result, &msgid)
	if err != nil {
		return Row{}, err
	}
	if thread.Valid {
		r.ThreadID = &thread.Int64
	}
	if replyTo.Valid {
		r.InReplyToMessageID = &replyTo.Int64
	}
	if sentAt.Valid {
		r.SentAt = &sentAt.Float64
	}
	if result.Valid {
		r.SMTPResult = &result.String
	}
	if msgid.Valid {
		r.SentMsgID = &msgid.String
	}
	r.Status = Status(status)
	if !r.Status.Valid() {
		r.Status = Invalid
	}
	return r, nil
}

func (s *Store) queryRows(ctx context.Context, query string, args ...any) ([]Row, error) {
	rows, err := s.read().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Row{}
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id int64) (*Row, error) {
	r, err := scanRow(s.read().QueryRowContext(ctx, "SELECT "+columns+" FROM drafts WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) ForMessage(ctx context.Context, messageID int64) (*Row, error) {
	r, err := scanRow(s.read().QueryRowContext(ctx,
		"SELECT d.id, d.thread_id, d.account, d.to_addrs, d.cc_addrs, d.subject, d.body, d.in_reply_to_message_id, "+
			"d.in_reply_to, d.refs, d.status, d.revision, d.created_at, d.updated_at, d.sent_at, d.smtp_result, d.sent_msgid "+
			"FROM drafts d JOIN messages m ON m.msgid = d.sent_msgid AND m.account = d.account WHERE m.id = ? AND d.sent_msgid != '' AND d.thread_id IS NOT NULL",
		messageID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) List(ctx context.Context, status Status, limit int) ([]Row, error) {
	return s.ListPage(ctx, status, 0, limit)
}

func (s *Store) ListPage(ctx context.Context, status Status, offset, limit int) ([]Row, error) {
	if status != "" {
		return s.queryRows(ctx, "SELECT "+columns+" FROM drafts WHERE status = ? ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?", string(status), limit, offset)
	}
	return s.queryRows(ctx, "SELECT "+columns+" FROM drafts ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?", limit, offset)
}

var lineBreakRE = regexp.MustCompile(`[\r\n]`)

func accountError(ctx context.Context) Error {
	accounts := config.From(ctx).Accounts()
	names := make([]string, len(accounts))
	for i, a := range accounts {
		names[i] = a.Name
	}
	return Error("account must be one of " + strings.Join(names, ", "))
}

func validAccount(ctx context.Context, v any) (string, bool) {
	name, ok := v.(string)
	if !ok {
		return "", false
	}
	_, found := config.From(ctx).Account(name)
	return name, found
}

func cleanLine(name, value string, limit int) (string, error) {
	value = strings.TrimSpace(value)
	if lineBreakRE.MatchString(value) {
		return "", Error(name + " must be a single line")
	}
	if utf8.RuneCountInString(value) > limit {
		return "", Error(fmt.Sprintf("%s is limited to %d characters", name, limit))
	}
	return value, nil
}

func NormalizeAddresses(name string, value any) (string, error) {
	var items []string
	switch v := value.(type) {
	case nil:
		return "", nil
	case string:
		if v == "" {
			return "", nil
		}
		items = []string{v}
	case []string:
		if len(v) == 0 {
			return "", nil
		}
		items = v
	case []any:
		if len(v) == 0 {
			return "", nil
		}
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return "", Error(name + " must be a list of addresses")
			}
			items = append(items, s)
		}
	default:
		return "", Error(name + " must be a list of addresses")
	}
	text, err := cleanLine(name, strings.Join(items, ", "), maxAddresses)
	if err != nil {
		return "", err
	}
	parsed, err := mime.ParseAddressList(text)
	if err != nil || len(parsed) == 0 {
		return "", Error(name + " has an invalid address")
	}
	out := make([]string, len(parsed))
	for i, p := range parsed {
		f, err := mime.FormatAddress(p)
		if err != nil {
			return "", Error(name + " has an invalid address")
		}
		out[i] = f
	}
	return strings.Join(out, ", "), nil
}

type reply struct {
	inReplyTo    string
	refs         string
	participants map[string]bool
	account      string
}

func rawFile(cacheDir, rawPath string) string {
	if rawPath == "" {
		return ""
	}
	root, err := filepath.Abs(cacheDir)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	path := rawPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return path
}

func (s *Store) replyContext(ctx context.Context, messageID int64) (reply, error) {
	var account, fromAddr, fromName string
	var msgid, toAddrs, rawPath sql.NullString
	err := s.read().QueryRowContext(ctx,
		"SELECT account, from_addr, from_name, msgid, to_addrs, raw_path FROM messages WHERE id = ?", messageID,
	).Scan(&account, &fromAddr, &fromName, &msgid, &toAddrs, &rawPath)
	if errors.Is(err, sql.ErrNoRows) {
		return reply{}, Error(fmt.Sprintf("no message %d", messageID))
	}
	if err != nil {
		return reply{}, err
	}
	headers := map[string]string{}
	var raw *mime.Message
	if path := rawFile(s.Cfg.CacheDir, rawPath.String); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return reply{}, err
		}
		raw = mime.Parse(data)
		for _, name := range [...]string{"From", "To", "Cc", "Reply-To", "Message-ID"} {
			headers[name] = raw.Header(name)
		}
	}
	if headers["From"] == "" {
		headers["From"] = fromAddr
		if fromName != "" {
			headers["From"] = fromName + " <" + fromAddr + ">"
		}
	}
	id := mime.ParseMsgID(msgid.String)
	if id == "" {
		id = mime.ParseMsgID(headers["Message-ID"])
	}
	inReplyTo := id.String()
	var chain []mime.MsgID
	if raw != nil {
		chain = mime.ParseMsgIDs(raw.Header("References"))
	}
	if id != "" && !slices.Contains(chain, id) {
		chain = append(chain, id)
	}
	refs := make([]string, len(chain))
	for i, mid := range chain {
		refs[i] = mid.String()
	}
	people := []string{fromAddr, headers["From"], headers["To"], headers["Cc"], headers["Reply-To"]}
	if toAddrs.String != "" {
		var listed []string
		if err := json.Unmarshal([]byte(toAddrs.String), &listed); err != nil {
			return reply{}, err
		}
		people = append(people, listed...)
	}
	participants := map[string]bool{}
	for _, p := range people {
		for _, a := range addresses(p) {
			participants[strings.ToLower(a)] = true
		}
	}
	return reply{inReplyTo: inReplyTo, refs: strings.Join(refs, " "), participants: participants, account: account}, nil
}

func (s *Store) limits(ctx context.Context) (maxBody, maxSubject int, err error) {
	return mail.TuningDraftsMaxBody, mail.TuningDraftsMaxSubject, nil
}

func (s *Store) Create(ctx context.Context, threadID *int64, account string, to, cc any, subject string, body any, replyTo any) (int64, error) {
	maxBody, maxSubject, err := s.limits(ctx)
	if err != nil {
		return 0, err
	}
	if _, ok := validAccount(ctx, account); !ok {
		return 0, accountError(ctx)
	}
	toAddrs, err := NormalizeAddresses("to", to)
	if err != nil {
		return 0, err
	}
	if toAddrs == "" {
		return 0, Error("to is required")
	}
	ccAddrs, err := NormalizeAddresses("cc", cc)
	if err != nil {
		return 0, err
	}
	subject, err = cleanLine("subject", subject, maxSubject)
	if err != nil {
		return 0, err
	}
	text, ok := body.(string)
	if !ok || strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > maxBody {
		return 0, Error(fmt.Sprintf("body must be 1..%d characters", maxBody))
	}
	var r reply
	var replyID any
	if replyTo != nil {
		var id int64
		if id, r, err = s.replyFor(ctx, replyTo); err != nil {
			return 0, err
		}
		replyID = id
	}
	ts := s.now()
	res, err := s.write().ExecContext(ctx,
		"INSERT INTO drafts (thread_id, account, to_addrs, cc_addrs, subject, body, in_reply_to_message_id, "+
			"in_reply_to, refs, status, revision, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		threadID, account, toAddrs, ccAddrs, subject, text, replyID, r.inReplyTo, r.refs, string(Draft), 1, ts, ts)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) replyFor(ctx context.Context, v any) (int64, reply, error) {
	var id int64
	shown := ""
	found := true
	switch n := v.(type) {
	case int:
		id = int64(n)
	case int64:
		id = n
	case *big.Int:
		return 0, reply{}, Error("message id is too large")
	case bool:
		shown = "False"
		if n {
			id, shown = 1, "True"
		}
	case float64:
		shown = strconv.FormatFloat(n, 'f', -1, 64)
		found = n == math.Trunc(n) && math.Abs(n) < 1<<63
		id = int64(n)
	case string:
		shown = n
		id, found = sqliteInteger(n)
	default:
		return 0, reply{}, Error(fmt.Sprintf("unsupported message id type %T", v))
	}
	if !found {
		return 0, reply{}, Error("no message " + shown)
	}
	r, err := s.replyContext(ctx, id)
	if _, plain := v.(int64); !plain && err != nil && err.Error() == fmt.Sprintf("no message %d", id) && shown != "" {
		return 0, reply{}, Error("no message " + shown)
	}
	return id, r, err
}

func (s *Store) Update(ctx context.Context, id int64, changes map[string]any) (*Row, error) {
	row, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, Error(fmt.Sprintf("no draft %d", id))
	}
	if row.Status != Draft {
		return nil, Error(fmt.Sprintf("draft is %s, only drafts can be edited", row.Status))
	}
	maxBody, maxSubject, err := s.limits(ctx)
	if err != nil {
		return nil, err
	}
	var sets []string
	var args []any
	set := func(col string, v any) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
	}
	if v, ok := changes["account"]; ok {
		switch v.(type) {
		case []any, []string, map[string]any:
			return nil, Error("account must be a single name")
		}
		name, valid := validAccount(ctx, v)
		if !valid {
			return nil, accountError(ctx)
		}
		set("account", name)
	}
	if v, ok := changes["to"]; ok {
		addrs, err := NormalizeAddresses("to", v)
		if err != nil {
			return nil, err
		}
		if addrs == "" {
			return nil, Error("to is required")
		}
		set("to_addrs", addrs)
	}
	if v, ok := changes["cc"]; ok {
		addrs, err := NormalizeAddresses("cc", v)
		if err != nil {
			return nil, err
		}
		set("cc_addrs", addrs)
	}
	if v, ok := changes["subject"]; ok {
		text, isText := v.(string)
		if !isText && truthy(v) {
			return nil, Error("subject must be text")
		}
		subject, err := cleanLine("subject", text, maxSubject)
		if err != nil {
			return nil, err
		}
		set("subject", subject)
	}
	if v, ok := changes["body"]; ok {
		body, isText := v.(string)
		if !isText || strings.TrimSpace(body) == "" || utf8.RuneCountInString(body) > maxBody {
			return nil, Error(fmt.Sprintf("body must be 1..%d characters", maxBody))
		}
		set("body", body)
	}
	if v, ok := changes["in_reply_to_message_id"]; ok {
		if v == nil {
			set("in_reply_to_message_id", nil)
			set("in_reply_to", "")
			set("refs", "")
		} else {
			replyTo, r, err := s.replyFor(ctx, v)
			if err != nil {
				return nil, err
			}
			set("in_reply_to_message_id", replyTo)
			set("in_reply_to", r.inReplyTo)
			set("refs", r.refs)
		}
	}
	if len(sets) == 0 {
		return nil, Error("nothing to change")
	}
	set("updated_at", s.now())
	sets = append(sets, "revision = revision + 1")
	args = append(args, id, string(Draft))
	res, err := s.write().ExecContext(ctx, "UPDATE drafts SET "+strings.Join(sets, ", ")+" WHERE id = ? AND status = ?", args...)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if n == 0 {
		return nil, Stale{}
	}
	return s.Get(ctx, id)
}

type updateSet struct {
	sets []string
	args []any
}

func (u *updateSet) set(col string, v any) {
	u.sets = append(u.sets, col+" = ?")
	u.args = append(u.args, v)
}

func (s *Store) transition(ctx context.Context, id int64, from, to Status, apply func(*updateSet)) error {
	return transitionTx(ctx, s.write(), s.now(), id, from, to, apply)
}

func transitionTx(ctx context.Context, x db.Querier, now float64, id int64, from, to Status, apply func(*updateSet)) error {
	u := &updateSet{}
	u.set("status", string(to))
	u.set("updated_at", now)
	u.sets = append(u.sets, "revision = revision + 1")
	if apply != nil {
		apply(u)
	}
	args := append(u.args, id, string(from))
	res, err := x.ExecContext(ctx, "UPDATE drafts SET "+strings.Join(u.sets, ", ")+" WHERE id = ? AND status = ?", args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return Stale{}
	}
	return nil
}

func (s *Store) exec(ctx context.Context, query string, args ...any) (int64, error) {
	res, err := s.write().ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) Discard(ctx context.Context, id int64) error {
	if err := s.transition(ctx, id, Draft, Discarded, nil); err != nil {
		var stale Stale
		if errors.As(err, &stale) {
			return Error("only an unsent draft can be discarded")
		}
		return err
	}
	return nil
}

func (s *Store) RecoverStuck(ctx context.Context) (int64, error) {
	return s.exec(ctx, "UPDATE drafts SET status = ?, smtp_result = ?, updated_at = ?, revision = revision + 1 WHERE status = ? AND updated_at < ?",
		string(Unknown), "send interrupted; outcome unknown", s.now(), string(Sending), s.now()-SendingStaleSeconds)
}

func (s *Store) ResolveUnknown(ctx context.Context, id int64, status Status, who ledger.Resolver) (int64, error) {
	if status != Sent && status != Draft {
		return 0, Error("an unknown draft can only be marked sent or returned to draft")
	}
	note := "returned to draft by operator"
	outcome := "not-applied"
	if status == Sent {
		note = "marked sent by operator"
		outcome = "applied"
	}
	row, err := s.Get(ctx, id)
	if err != nil {
		return 0, err
	}
	if row == nil {
		return 0, Error("draft is not in the unknown state")
	}
	store := s.ledgerStore()
	if _, err := store.Recover(ctx, s.now()-ledger.StuckSeconds); err != nil {
		return 0, err
	}
	effect, err := ledger.BlockedEffectID(ctx, s.read(), SendOperation, SendTarget(row.Account, id))
	if err != nil {
		return 0, err
	}
	apply := func(ctx context.Context, tx *sql.Tx) error {
		return resolveDraftTx(ctx, tx, s.now(), id, status, note)
	}
	if effect == 0 {
		return 0, s.DB.WithTx(ctx, func(tx *sql.Tx) error { return apply(ctx, tx) })
	}
	_, err = store.Resolve(ctx, effect, ledger.Resolution{Outcome: outcome}, who, apply)
	return effect, err
}

func resolveDraftTx(ctx context.Context, x db.Querier, now float64, id int64, status Status, note string) error {
	res, err := x.ExecContext(ctx,
		"UPDATE drafts SET status = ?, sent_at = CASE WHEN ? = ? THEN ? ELSE sent_at END, smtp_result = ?, updated_at = ?, revision = revision + 1 "+
			"WHERE id = ? AND status = ?",
		string(status), string(status), string(Sent), now, note, now, id, string(Unknown))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return Error("draft is not in the unknown state")
	}
	return nil
}

func (s *Store) UnexpectedRecipients(ctx context.Context, d Row) ([]string, error) {
	if d.InReplyToMessageID == nil {
		return []string{}, nil
	}
	r, err := s.replyContext(ctx, *d.InReplyToMessageID)
	var derr Error
	if errors.As(err, &derr) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	return unexpectedFrom(r, d), nil
}

func (s *Store) UnexpectedRecipientsBatch(ctx context.Context, rows []Row) ([][]string, error) {
	out := make([][]string, len(rows))
	cache := map[int64]reply{}
	for i, row := range rows {
		if row.InReplyToMessageID == nil {
			out[i] = []string{}
			continue
		}
		id := *row.InReplyToMessageID
		r, ok := cache[id]
		if !ok {
			var err error
			r, err = s.replyContext(ctx, id)
			var derr Error
			if errors.As(err, &derr) {
				r = reply{}
			} else if err != nil {
				return nil, err
			}
			cache[id] = r
		}
		out[i] = unexpectedFrom(r, row)
	}
	return out, nil
}

func unexpectedFrom(r reply, d Row) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, a := range append(addresses(d.ToAddrs), addresses(d.CcAddrs)...) {
		if seen[a] {
			continue
		}
		seen[a] = true
		if !r.participants[strings.ToLower(a)] {
			out = append(out, a)
		}
	}
	return out
}

func addresses(text string) []string {
	parsed, err := mime.ParseAddressList(text)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(parsed))
	for _, a := range parsed {
		if a.Address != "" {
			out = append(out, a.Address)
		}
	}
	return out
}

func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case string:
		return v != ""
	case bool:
		return v
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

func sqliteInteger(text string) (int64, bool) {
	text = strings.Trim(text, " \t\n\v\f\r")
	if text == "" || strings.Trim(text, "0123456789+-.eE") != "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return n, true
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || f != math.Trunc(f) || math.Abs(f) >= 1<<63 {
		return 0, false
	}
	return int64(f), true
}
