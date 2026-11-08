package message

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/mail"
	"os"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/features"
	"github.com/jeiang/atrium/internal/mail/mime"
	"github.com/jeiang/atrium/internal/py"
)

const (
	sourceRaw   = "raw"
	sourceIndex = "index"
)

var Headers = [...]string{"From", "To", "Cc", "Reply-To", "Date", "Subject", "Message-ID", "List-Id"}

type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
}

type View struct {
	ID           int64             `json:"id"`
	Account      string            `json:"account"`
	Headers      map[string]string `json:"headers"`
	DateTS       float64           `json:"date_ts"`
	FromAddr     string            `json:"from_addr"`
	FromName     string            `json:"from_name"`
	Subject      string            `json:"subject"`
	Body         string            `json:"body"`
	BodySource   string            `json:"body_source"`
	Attachments  []Attachment      `json:"attachments"`
	Location     string            `json:"location"`
	Folder       string            `json:"folder"`
	CorrectedTo  *string           `json:"corrected_to"`
	InInbox      bool              `json:"in_inbox"`
	Seen         bool              `json:"seen"`
	Flagged      bool              `json:"flagged"`
	Decision     map[string]any    `json:"decision"`
	FlagDecision map[string]any    `json:"flag_decision"`
}

type Querier = db.Querier

type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type DecisionRecord struct {
	MessageID  int64
	Account    string
	TS         float64
	Stage      string
	Key        *string
	Action     string
	Dest       *string
	Confidence *float64
	Detail     any
}

func RecordDecision(ctx context.Context, q Execer, d DecisionRecord) error {
	detail, err := json.Marshal(d.Detail)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx,
		"INSERT INTO decisions (message_id, account, ts, stage, key, action, dest, confidence, detail) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		d.MessageID, d.Account, d.TS, d.Stage, nullableString(d.Key), d.Action, nullableString(d.Dest), nullableFloat(d.Confidence),
		string(detail))
	return err
}

func nullableString(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func RawFile(cacheDir, rawPath string) (string, bool) {
	if rawPath == "" {
		return "", false
	}
	root := py.Resolve(cacheDir)
	target := rawPath
	if !strings.HasPrefix(rawPath, "/") {
		target = root + "/" + rawPath
	}
	resolved := py.Resolve(target)
	if resolved != root && !strings.HasPrefix(resolved, strings.TrimSuffix(root, "/")+"/") {
		return "", false
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return resolved, true
}

func displayAddresses(addrs []mail.Address) string {
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = a.String()
	}
	return strings.Join(parts, ", ")
}

func plainBody(msg *mime.Message) string {
	for _, c := range [...]struct {
		kind    string
		convert func(string) string
	}{{"plain", strings.TrimSpace}, {"html", features.StripMarkup}} {
		part := msg.Body(c.kind)
		if part == nil {
			continue
		}
		if text := c.convert(part.Content()); text != "" {
			return text
		}
	}
	return ""
}

func attachments(msg *mime.Message) []Attachment {
	found := []Attachment{}
	for _, part := range msg.Attachments() {
		size := 0
		if !part.IsMultipart() {
			size = len(part.Payload())
		}
		found = append(found, Attachment{Filename: part.Filename(), ContentType: part.ContentType(), Size: size})
	}
	return found
}

func rowMap(rows *sql.Rows) (map[string]any, error) {
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		return nil, rows.Err()
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	out := make(map[string]any, len(cols))
	for i, c := range cols {
		if b, ok := vals[i].([]byte); ok {
			vals[i] = string(b)
		}
		out[c] = vals[i]
	}
	return out, rows.Err()
}

func latestDecision(ctx context.Context, q Querier, messageID int64) (map[string]any, error) {
	rows, err := q.QueryContext(ctx, "SELECT * FROM decisions WHERE message_id = ? ORDER BY id DESC LIMIT 1", messageID)
	if err != nil {
		return nil, err
	}
	row, err := rowMap(rows)
	if err != nil || row == nil {
		return nil, err
	}
	detail, _ := row["detail"].(string)
	if detail == "" {
		detail = "{}"
	}
	dec := json.NewDecoder(strings.NewReader(detail))
	dec.UseNumber()
	var parsed any
	if err := dec.Decode(&parsed); err != nil {
		return nil, err
	}
	row["detail"] = parsed
	return row, nil
}

func Body(ctx context.Context, q Querier, messageID int64) (string, error) {
	return indexBody(ctx, q, messageID)
}

func indexBody(ctx context.Context, q Querier, messageID int64) (string, error) {
	var body string
	err := q.QueryRowContext(ctx, "SELECT body FROM message_fts WHERE rowid = ?", messageID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return body, err
}

func Get(ctx context.Context, q Querier, cacheDir string, messageID int64) (*View, error) {
	var v View
	var rawPath, folder, corrected sql.NullString
	var inInbox, seen, flagged int
	err := q.QueryRowContext(ctx,
		"SELECT m.id, m.account, m.date_ts, m.from_addr, m.from_name, m.subject, m.raw_path, m.location, m.folder, "+
			"m.in_inbox, m.seen, m.flagged, c.folder AS corrected_to "+
			"FROM messages m LEFT JOIN corrections c ON c.message_id = m.id WHERE m.id = ?", messageID).
		Scan(&v.ID, &v.Account, &v.DateTS, &v.FromAddr, &v.FromName, &v.Subject, &rawPath, &v.Location, &folder,
			&inInbox, &seen, &flagged, &corrected)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v.Headers = make(map[string]string, len(Headers))
	for _, name := range Headers {
		v.Headers[name] = ""
	}
	v.Attachments = []Attachment{}
	v.BodySource = sourceIndex
	if path, ok := RawFile(cacheDir, rawPath.String); ok {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		msg := mime.Parse(data)
		for _, name := range Headers {
			switch name {
			case "From", "To", "Cc", "Reply-To":
				v.Headers[name] = displayAddresses(msg.Addresses(name))
			default:
				v.Headers[name] = msg.Header(name)
			}
		}
		v.Body, v.Attachments, v.BodySource = plainBody(msg), attachments(msg), sourceRaw
	}
	if v.Body == "" {
		if v.Body, err = indexBody(ctx, q, messageID); err != nil {
			return nil, err
		}
		v.BodySource = sourceIndex
	}
	if v.Headers["Subject"] == "" {
		v.Headers["Subject"] = v.Subject
	}
	if v.Headers["From"] == "" {
		v.Headers["From"] = v.FromAddr
		if v.FromName != "" {
			v.Headers["From"] = v.FromName + " <" + v.FromAddr + ">"
		}
	}
	rows, err := q.QueryContext(ctx, "SELECT * FROM flag_decisions WHERE message_id = ?", messageID)
	if err != nil {
		return nil, err
	}
	if v.FlagDecision, err = rowMap(rows); err != nil {
		return nil, err
	}
	if v.Decision, err = latestDecision(ctx, q, messageID); err != nil {
		return nil, err
	}
	v.InInbox, v.Seen, v.Flagged = inInbox != 0, seen != 0, flagged != 0
	if corrected.Valid {
		v.CorrectedTo = &corrected.String
	}
	v.Folder = v.Location
	switch {
	case corrected.String != "":
		v.Folder = corrected.String
	case folder.String != "":
		v.Folder = folder.String
	}
	return &v, nil
}
