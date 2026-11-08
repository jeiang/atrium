package embed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/py"
)

const (
	KNNMax = 4096

	VisibilityPublic  = "public"
	VisibilityPrivate = "private"
)

var log = logx.Logger("atrium.embed")

type TextsFunc func(ctx context.Context, url string, texts []string) ([][]float64, error)

func DocumentText(ctx context.Context, subject, name, addr, body string) string {
	if subject == "" {
		subject = "none"
	}
	from := addr
	if name != "" {
		from = name + " <" + addr + ">"
	}
	return "title: " + subject + " | text: From: " + from + "\n\n" + py.Clip(body, endpoint.From(ctx).EmbedBodyChars)
}

func NotionDocumentText(ctx context.Context, title, path, markdown string) string {
	if title == "" {
		title = "none"
	}
	return "title: " + title + " | text: " + path + "\n\n" + py.Clip(markdown, endpoint.From(ctx).EmbedBodyChars)
}

func QueryText(query string) string {
	return "task: search result | query: " + query
}

type embeddingsRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingsReply struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

func Texts(ctx context.Context, url string, texts []string) ([][]float64, error) {
	s := endpoint.From(ctx)
	raw, err := httpx.PostJSON(ctx, url+"/embeddings", embeddingsRequest{s.EmbedModel, texts})
	if err != nil {
		return nil, err
	}
	var reply embeddingsReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("%w: %w", httpx.ErrPayload, err)
	}
	if len(reply.Data) != len(texts) {
		return nil, fmt.Errorf("%w: embedding response has %d vectors for %d texts", httpx.ErrPayload, len(reply.Data), len(texts))
	}
	out := make([][]float64, len(texts))
	seen := make([]bool, len(texts))
	for _, entry := range reply.Data {
		if entry.Index < 0 || entry.Index >= len(texts) || seen[entry.Index] {
			return nil, fmt.Errorf("%w: embedding response index %d is out of range or repeated", httpx.ErrPayload, entry.Index)
		}
		if len(entry.Embedding) != s.Dim {
			return nil, fmt.Errorf("%w: embedding response shape mismatch: expected %dx%d", httpx.ErrPayload, len(texts), s.Dim)
		}
		seen[entry.Index] = true
		out[entry.Index] = Normalize(entry.Embedding)
	}
	return out, nil
}

func Normalize(vector []float64) []float64 {
	norm := math.Sqrt(py.SumSq(vector))
	if norm == 0 {
		norm = 1
	}
	out := make([]float64, len(vector))
	for i, x := range vector {
		out[i] = x / norm
	}
	return out
}

func Serialize(vector []float64) []byte {
	f := make([]float32, len(vector))
	for i, x := range vector {
		f[i] = float32(x)
	}
	return db.SerializeFloat32(f)
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func pyStr(s sql.NullString) string {
	if !s.Valid {
		return "None"
	}
	return s.String
}

func MessageDocument(ctx context.Context, q querier, id int64) (string, error) {
	var subject, name, addr, body sql.NullString
	err := q.QueryRowContext(ctx,
		"SELECT m.subject, m.from_name, m.from_addr, f.body FROM messages m "+
			"JOIN message_fts f ON f.rowid = m.id WHERE m.id = ?", id).Scan(&subject, &name, &addr, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: message %d has no index row: 'NoneType' object is not subscriptable", httpx.ErrShape, id)
	}
	if err != nil {
		return "", err
	}
	return DocumentText(ctx, subject.String, pyStr(name), pyStr(addr), body.String), nil
}

type pendingMessage struct {
	id      int64
	account string
}

func Pending(ctx context.Context, d *db.DB, url string) (int, error) {
	done := 0
	for {
		idx, err := Active(ctx, d.Read)
		if err != nil {
			return done, err
		}
		n, err := messageBatch(ctx, d, url, idx, "embedded = 0", true)
		done += n
		if err != nil || n == 0 {
			return done, err
		}
		log.Info(fmt.Sprintf("embedded %d", done))
	}
}

func FillMessages(ctx context.Context, d *db.DB, url string, idx Index) (int, error) {
	return messageBatch(ctx, d, url, idx, "id NOT IN (SELECT rowid FROM "+idx.MessageTable()+") AND id IN (SELECT rowid FROM message_fts)", false)
}

func messageBatch(ctx context.Context, d *db.DB, url string, idx Index, where string, mark bool) (int, error) {
	rows, err := d.Read.QueryContext(ctx,
		"SELECT id, account FROM messages WHERE "+where+" AND gone_at IS NULL ORDER BY id LIMIT ?", endpoint.From(ctx).Batch)
	if err != nil {
		return 0, err
	}
	batch, err := collect(rows, func(r *sql.Rows) (pendingMessage, error) {
		var m pendingMessage
		err := r.Scan(&m.id, &m.account)
		return m, err
	})
	if err != nil || len(batch) == 0 {
		return 0, err
	}
	texts := make([]string, len(batch))
	for i, m := range batch {
		if texts[i], err = MessageDocument(ctx, d.Read, m.id); err != nil {
			return 0, err
		}
	}
	vectors, err := Texts(ctx, url, texts)
	if err != nil {
		return 0, err
	}
	table := idx.MessageTable()
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		for i, m := range batch {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE rowid = ?", m.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO "+table+" (rowid, account, embedding) VALUES (?, ?, ?)",
				m.id, m.account, Serialize(vectors[i])); err != nil {
				return err
			}
			if !mark {
				continue
			}
			if _, err := tx.ExecContext(ctx, "UPDATE messages SET embedded = 1 WHERE id = ?", m.id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(batch), nil
}

func PendingOrWarn(ctx context.Context, d *db.DB, url string, passLog *slog.Logger) error {
	_, err := Pending(ctx, d, url)
	var unavailable *httpx.ServiceUnavailable
	if errors.As(err, &unavailable) {
		passLog.Warn(fmt.Sprintf("embedding unavailable: %s", unavailable))
		return nil
	}
	return err
}

func Reindex(ctx context.Context, d *db.DB, url string) (int, error) {
	idx, err := Active(ctx, d.Read)
	if err != nil {
		return 0, err
	}
	if _, err := d.Write.ExecContext(ctx, "DELETE FROM "+idx.MessageTable()); err != nil {
		return 0, err
	}
	if _, err := d.Write.ExecContext(ctx, "UPDATE messages SET embedded = 0"); err != nil {
		return 0, err
	}
	return Pending(ctx, d, url)
}

type pendingPage struct {
	rid                   int64
	title, path, markdown string
	private               int64
}

func NotionPending(ctx context.Context, d *db.DB, url string, texts TextsFunc) (int, error) {
	done := 0
	for {
		idx, err := Active(ctx, d.Read)
		if err != nil {
			return done, err
		}
		n, err := notionBatch(ctx, d, url, texts, idx, "embedded = 0", true)
		done += n
		if err != nil || n == 0 {
			return done, err
		}
	}
}

func FillNotion(ctx context.Context, d *db.DB, url string, idx Index) (int, error) {
	return notionBatch(ctx, d, url, Texts, idx, "rid NOT IN (SELECT rowid FROM "+idx.NotionTable()+")", false)
}

func notionBatch(ctx context.Context, d *db.DB, url string, texts TextsFunc, idx Index, where string, mark bool) (int, error) {
	rows, err := d.Read.QueryContext(ctx,
		"SELECT rid, title, path, markdown, private FROM notion_pages WHERE "+where+" AND in_trash = 0 ORDER BY rid LIMIT ?",
		endpoint.From(ctx).Batch)
	if err != nil {
		return 0, err
	}
	batch, err := collect(rows, func(r *sql.Rows) (pendingPage, error) {
		var p pendingPage
		err := r.Scan(&p.rid, &p.title, &p.path, &p.markdown, &p.private)
		return p, err
	})
	if err != nil || len(batch) == 0 {
		return 0, err
	}
	docs := make([]string, len(batch))
	for i, p := range batch {
		docs[i] = NotionDocumentText(ctx, p.title, p.path, p.markdown)
	}
	vectors, err := texts(ctx, url, docs)
	if err != nil {
		return 0, err
	}
	if len(vectors) != len(batch) {
		return 0, fmt.Errorf("%w: zip() argument 2 is shorter or longer than argument 1", httpx.ErrPayload)
	}
	table := idx.NotionTable()
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		for i, p := range batch {
			visibility := VisibilityPublic
			if p.private != 0 {
				visibility = VisibilityPrivate
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE rowid = ?", p.rid); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO "+table+" (rowid, visibility, embedding) VALUES (?, ?, ?)",
				p.rid, visibility, Serialize(vectors[i])); err != nil {
				return err
			}
			if !mark {
				continue
			}
			if _, err := tx.ExecContext(ctx, "UPDATE notion_pages SET embedded = 1 WHERE rid = ?", p.rid); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(batch), nil
}

func collect[T any](rows *sql.Rows, scan func(*sql.Rows) (T, error)) ([]T, error) {
	defer func() { _ = rows.Close() }()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
