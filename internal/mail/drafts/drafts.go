// Package drafts is the draft lifecycle (src/atrium/drafts.py): the agent and the operator write drafts, and
// only the operator's Send sends one, through internal/mail/smtp, filing the iCloud copy in Sent under the
// operator-send write scope.
package drafts

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mime"
	"github.com/jeiang/atrium/internal/mail/smtp"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

const (
	Draft     = "draft"
	Sending   = "sending"
	Sent      = "sent"
	Discarded = "discarded"
	Unknown   = "unknown"

	SendingStaleSeconds = 600
	MaxBody             = 100_000
	MaxSubject          = 300
	maxAddresses        = 2000
)

var Statuses = [...]string{Draft, Sending, Sent, Discarded, Unknown}

var log = logx.Logger("atrium.drafts")

// Error is DraftError: a refused draft operation, worded for the operator.
type Error string

func (e Error) Error() string { return string(e) }

// Row is one drafts row.
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
	Status             string   `json:"status"`
	CreatedAt          float64  `json:"created_at"`
	UpdatedAt          float64  `json:"updated_at"`
	SentAt             *float64 `json:"sent_at"`
	SMTPResult         *string  `json:"smtp_result"`
	SentMsgID          *string  `json:"sent_msgid"`
}

// Opener connects a write-gated IMAP client for an account (run.open_client).
type Opener func(ctx context.Context, account string) (mail.Client, error)

// Store runs the draft operations on atrium.db. Open files the iCloud sent copy; SMTP, MsgID and Now default to
// the real SMTP dialer, email.utils.make_msgid and the wall clock.
type Store struct {
	DB    *db.DB
	Cfg   config.Config
	Open  Opener
	SMTP  smtp.Factory
	MsgID func(domain string) string
	Now   func() float64
}

func (s *Store) now() float64 {
	if s.Now != nil {
		return s.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

const columns = "id, thread_id, account, to_addrs, cc_addrs, subject, body, in_reply_to_message_id, in_reply_to, refs, " +
	"status, created_at, updated_at, sent_at, smtp_result, sent_msgid"

type scanner interface{ Scan(dest ...any) error }

func scanRow(sc scanner) (Row, error) {
	var r Row
	var thread, replyTo sql.NullInt64
	var sentAt sql.NullFloat64
	var result, msgid sql.NullString
	err := sc.Scan(&r.ID, &thread, &r.Account, &r.ToAddrs, &r.CcAddrs, &r.Subject, &r.Body, &replyTo, &r.InReplyTo, &r.Refs,
		&r.Status, &r.CreatedAt, &r.UpdatedAt, &sentAt, &result, &msgid)
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
	return r, nil
}

func (s *Store) queryRows(ctx context.Context, query string, args ...any) ([]Row, error) {
	rows, err := s.DB.Read.QueryContext(ctx, query, args...)
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

// Get is the draft with id, or nil when there is none.
func (s *Store) Get(ctx context.Context, id int64) (*Row, error) {
	r, err := scanRow(s.DB.Read.QueryRowContext(ctx, "SELECT "+columns+" FROM drafts WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ForMessage is the thread-made draft a mailbox message is the sent copy of (by Message-ID), or nil.
func (s *Store) ForMessage(ctx context.Context, messageID int64) (*Row, error) {
	r, err := scanRow(s.DB.Read.QueryRowContext(ctx,
		"SELECT d.id, d.thread_id, d.account, d.to_addrs, d.cc_addrs, d.subject, d.body, d.in_reply_to_message_id, "+
			"d.in_reply_to, d.refs, d.status, d.created_at, d.updated_at, d.sent_at, d.smtp_result, d.sent_msgid "+
			"FROM drafts d JOIN messages m ON m.msgid = d.sent_msgid WHERE m.id = ? AND d.sent_msgid != '' AND d.thread_id IS NOT NULL",
		messageID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// List is list_drafts: the newest-updated drafts first, of one status or all when status is "".
func (s *Store) List(ctx context.Context, status string, limit int) ([]Row, error) {
	if status != "" {
		return s.queryRows(ctx, "SELECT "+columns+" FROM drafts WHERE status = ? ORDER BY updated_at DESC LIMIT ?", status, limit)
	}
	return s.queryRows(ctx, "SELECT "+columns+" FROM drafts ORDER BY updated_at DESC LIMIT ?", limit)
}

var (
	lineBreakRE = rx.MustCompile(`[\r\n]`, 0)
	msgidJunkRE = rx.MustCompile(`[<>\s]`, 0)
)

func accountError() Error {
	names := make([]string, len(config.Accounts))
	for i, a := range config.Accounts {
		names[i] = a.Name
	}
	return Error("account must be one of " + strings.Join(names, ", "))
}

func validAccount(v any) (string, bool) {
	name, ok := v.(string)
	if !ok {
		return "", false
	}
	_, found := config.Account(name)
	return name, found
}

func cleanLine(name, value string, limit int) (string, error) {
	value = py.Strip(value)
	if m, _ := lineBreakRE.Search(value); m != nil {
		return "", Error(name + " must be a single line")
	}
	if utf8.RuneCountInString(value) > limit {
		return "", Error(fmt.Sprintf("%s is limited to %d characters", name, limit))
	}
	return value, nil
}

// NormalizeAddresses is normalize_addresses: a list or one comma-separated string becomes "a@x, B <b@y>"; every
// entry must parse to an address with an @ and no space.
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
	parsed, err := mime.GetAddresses([]string{text})
	if err != nil {
		return "", err
	}
	for _, p := range parsed {
		if p.Address == "" || !strings.Contains(p.Address, "@") || strings.Contains(p.Address, " ") {
			return "", Error(name + " has an invalid address")
		}
	}
	out := make([]string, len(parsed))
	for i, p := range parsed {
		if p.Name == "" {
			out[i] = p.Address
			continue
		}
		f, err := formatAddr(p.Name, p.Address)
		if err != nil {
			return "", err
		}
		out[i] = f
	}
	return strings.Join(out, ", "), nil
}

// formatAddr is email.utils.formataddr((name, address)) with a non-empty name.
func formatAddr(name, address string) (string, error) {
	if err := py.ASCIIEncodeError(address); err != nil {
		return "", err
	}
	if py.ASCIIEncodeError(name) != nil {
		return headerEncode([]byte(name)) + " <" + address + ">", nil
	}
	quote := ""
	if strings.ContainsAny(name, `][\()<>@,:;".`) {
		quote = `"`
	}
	name = strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name)
	return quote + name + quote + " <" + address + ">", nil
}

func qpHeaderByte(c byte) string {
	switch {
	case c == ' ':
		return "_"
	case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-!*+/", c) >= 0:
		return string(c)
	}
	return fmt.Sprintf("=%02X", c)
}

// headerEncode is Charset('utf-8').header_encode: base64 when it is strictly shorter than the header Q encoding,
// Q otherwise.
func headerEncode(b []byte) string {
	var q strings.Builder
	for _, c := range b {
		q.WriteString(qpHeaderByte(c))
	}
	if base64.StdEncoding.EncodedLen(len(b)) < q.Len() {
		return "=?utf-8?b?" + base64.StdEncoding.EncodeToString(b) + "?="
	}
	return "=?utf-8?q?" + q.String() + "?="
}

type reply struct {
	inReplyTo    string
	refs         string
	participants map[string]bool
	account      string
}

// rawFile is message.raw_file: the cached .eml under the cache directory, or "" when absent or outside it.
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

func header(m *mime.Message, name string) string {
	v, ok, err := m.Get(name)
	if !ok || err != nil {
		return ""
	}
	return v
}

// replyContext is reply_context: In-Reply-To, References and the thread participants from the indexed
// original.
func (s *Store) replyContext(ctx context.Context, messageID int64) (reply, error) {
	var account, fromAddr, fromName string
	var msgid, toAddrs, rawPath sql.NullString
	err := s.DB.Read.QueryRowContext(ctx,
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
		if raw, err = mime.Parse(data); err != nil {
			return reply{}, err
		}
		for _, name := range [...]string{"From", "To", "Cc", "Reply-To", "Message-ID"} {
			headers[name] = header(raw, name)
		}
	}
	if headers["From"] == "" {
		headers["From"] = fromAddr
		if fromName != "" {
			headers["From"] = fromName + " <" + fromAddr + ">"
		}
	}
	id := msgid.String
	if id == "" {
		id = headers["Message-ID"]
	}
	id = py.Strip(id)
	references := ""
	if raw != nil {
		v, ok, err := raw.Get("References")
		if err != nil {
			return reply{}, err
		}
		if ok {
			references = strings.Join(py.Split(v), " ")
		}
	}
	var chain []string
	for _, x := range [...]string{references, id} {
		if x != "" {
			chain = append(chain, x)
		}
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
		addrs, err := smtp.SplitAddresses(p)
		if err != nil {
			return reply{}, err
		}
		for _, a := range addrs {
			participants[mime.Lower(a)] = true
		}
	}
	return reply{inReplyTo: id, refs: strings.Join(chain, " "), participants: participants, account: account}, nil
}

// Create is create: a new draft in status draft, threaded to replyTo when it is set.
func (s *Store) Create(ctx context.Context, threadID *int64, account string, to, cc any, subject string, body any, replyTo any) (int64, error) {
	if _, ok := validAccount(account); !ok {
		return 0, accountError()
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
	subject, err = cleanLine("subject", subject, MaxSubject)
	if err != nil {
		return 0, err
	}
	text, _ := body.(string)
	if py.Strip(text) == "" {
		return 0, Error("body is required")
	}
	if utf8.RuneCountInString(text) > MaxBody {
		return 0, Error(fmt.Sprintf("body is limited to %d characters", MaxBody))
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
	res, err := s.DB.Write.ExecContext(ctx,
		"INSERT INTO drafts (thread_id, account, to_addrs, cc_addrs, subject, body, in_reply_to_message_id, "+
			"in_reply_to, refs, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		threadID, account, toAddrs, ccAddrs, subject, text, replyID, r.inReplyTo, r.refs, Draft, ts, ts)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// replyFor is reply_context for the value a caller passed as in_reply_to_message_id. The value is bound to
// SQLite as Python's sqlite3 binds it: an int, a bool, a whole float or a numeric string finds the message of
// that id, any other float or string finds none, and a list or dict is a ProgrammingError. It returns the id
// the draft stores.
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
		return 0, reply{}, &py.Exception{Class: "OverflowError", Msg: "Python int too large to convert to SQLite INTEGER"}
	case bool:
		shown = "False"
		if n {
			id, shown = 1, "True"
		}
	case float64:
		shown = py.FloatRepr(n)
		found = n == math.Trunc(n) && math.Abs(n) < 1<<63
		id = int64(n)
	case string:
		shown = n
		id, found = sqliteInteger(n)
	default:
		return 0, reply{}, &py.Exception{Class: "ProgrammingError", Msg: "Error binding parameter 1: type '" + py.TypeName(v) + "' is not supported"}
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

// Update is update: changes holds any of account, to, cc, subject, body and in_reply_to_message_id; only a
// draft in status draft changes.
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
	var sets []string
	var args []any
	set := func(col string, v any) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
	}
	if v, ok := changes["account"]; ok {
		switch v.(type) {
		case []any, []string, py.Dict, map[string]any:
			return nil, &py.Exception{Class: "TypeError", Msg: "unhashable type: '" + py.TypeName(v) + "'"}
		}
		name, valid := validAccount(v)
		if !valid {
			return nil, accountError()
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
		if !isText && py.Truthy(v) {
			return nil, &py.Exception{Class: "AttributeError", Msg: "'" + py.TypeName(v) + "' object has no attribute 'strip'"}
		}
		subject, err := cleanLine("subject", text, MaxSubject)
		if err != nil {
			return nil, err
		}
		set("subject", subject)
	}
	if v, ok := changes["body"]; ok {
		body, isText := v.(string)
		if !isText || py.Strip(body) == "" || utf8.RuneCountInString(body) > MaxBody {
			return nil, Error(fmt.Sprintf("body must be 1..%d characters", MaxBody))
		}
		set("body", body)
	}
	if v := changes["in_reply_to_message_id"]; v != nil {
		replyTo, r, err := s.replyFor(ctx, v)
		if err != nil {
			return nil, err
		}
		set("in_reply_to_message_id", replyTo)
		set("in_reply_to", r.inReplyTo)
		set("refs", r.refs)
	}
	if len(sets) == 0 {
		return nil, Error("nothing to change")
	}
	set("updated_at", s.now())
	args = append(args, id, Draft)
	if _, err := s.DB.Write.ExecContext(ctx, "UPDATE drafts SET "+strings.Join(sets, ", ")+" WHERE id = ? AND status = ?", args...); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *Store) exec(ctx context.Context, query string, args ...any) (int64, error) {
	res, err := s.DB.Write.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Discard is discard: only a draft in status draft can be discarded.
func (s *Store) Discard(ctx context.Context, id int64) error {
	n, err := s.exec(ctx, "UPDATE drafts SET status = ?, updated_at = ? WHERE id = ? AND status = ?", Discarded, s.now(), id, Draft)
	if err != nil {
		return err
	}
	if n == 0 {
		return Error("only an unsent draft can be discarded")
	}
	return nil
}

// RecoverStuck is recover_stuck, run at web start: a draft left sending for more than SendingStaleSeconds
// may or may not have gone out, so it becomes unknown for the operator and is never resent.
func (s *Store) RecoverStuck(ctx context.Context) (int64, error) {
	return s.exec(ctx, "UPDATE drafts SET status = ?, smtp_result = ?, updated_at = ? WHERE status = ? AND updated_at < ?",
		Unknown, "send interrupted; outcome unknown", s.now(), Sending, s.now()-SendingStaleSeconds)
}

// ResolveUnknown is resolve_unknown: the operator's verdict on an unknown draft after checking the Sent folder.
func (s *Store) ResolveUnknown(ctx context.Context, id int64, status string) error {
	if status != Sent && status != Draft {
		return Error("an unknown draft can only be marked sent or returned to draft")
	}
	note := "returned to draft by operator"
	if status == Sent {
		note = "marked sent by operator"
	}
	n, err := s.exec(ctx,
		"UPDATE drafts SET status = ?, sent_at = CASE WHEN ? = ? THEN ? ELSE sent_at END, smtp_result = ?, updated_at = ? "+
			"WHERE id = ? AND status = ?",
		status, status, Sent, s.now(), note, s.now(), id, Unknown)
	if err != nil {
		return err
	}
	if n == 0 {
		return Error("draft is not in the unknown state")
	}
	return nil
}

// UnexpectedRecipients is unexpected_recipients: the To/Cc addresses of a reply that were not on the original.
func (s *Store) UnexpectedRecipients(ctx context.Context, d Row) ([]string, error) {
	out := []string{}
	if d.InReplyToMessageID == nil {
		return out, nil
	}
	r, err := s.replyContext(ctx, *d.InReplyToMessageID)
	var derr Error
	if errors.As(err, &derr) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	to, err := smtp.SplitAddresses(d.ToAddrs)
	if err != nil {
		return nil, err
	}
	cc, err := smtp.SplitAddresses(d.CcAddrs)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, a := range append(to, cc...) {
		if seen[a] {
			continue
		}
		seen[a] = true
		if !r.participants[mime.Lower(a)] {
			out = append(out, a)
		}
	}
	return out, nil
}

// sqliteInteger is how SQLite compares text with an INTEGER PRIMARY KEY: numeric text, with ASCII blanks around it,
// that converts to an integer without loss is that integer.
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
