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

type source[T any] struct {
	pending   string
	missing   func(Index) string
	query     func(string) string
	scan      func(*sql.Rows) (T, error)
	document  func(context.Context, T) string
	table     func(Index) string
	insert    string
	mark      string
	id        func(T) int64
	partition func(T) string
}

type messageRow struct {
	id      int64
	account string
	subject string
	name    string
	addr    string
	body    string
}

var messageSource = source[messageRow]{
	pending: "m.embedded = 0",
	missing: func(i Index) string { return "m.id NOT IN (SELECT rowid FROM " + i.MessageTable() + ")" },
	query: func(where string) string {
		return "SELECT m.id, m.account, m.subject, m.from_name, m.from_addr, f.body FROM messages m " +
			"JOIN message_fts f ON f.rowid = m.id WHERE " + where + " AND m.gone_at IS NULL ORDER BY m.id LIMIT ?"
	},
	scan: func(r *sql.Rows) (messageRow, error) {
		var m messageRow
		var subject, name, addr, body sql.NullString
		err := r.Scan(&m.id, &m.account, &subject, &name, &addr, &body)
		m.subject, m.name, m.addr, m.body = subject.String, name.String, addr.String, body.String
		return m, err
	},
	document: func(ctx context.Context, m messageRow) string {
		return DocumentText(ctx, m.subject, m.name, m.addr, m.body)
	},
	table:  func(i Index) string { return i.MessageTable() },
	insert: "INSERT INTO %s (rowid, account, embedding) VALUES (?, ?, ?)",
	mark:   "UPDATE messages SET embedded = 1 WHERE id = ?",
	id:     func(m messageRow) int64 { return m.id },
	partition: func(m messageRow) string {
		return m.account
	},
}

type notionRow struct {
	rid                   int64
	title, path, markdown string
	private               int64
}

var notionSource = source[notionRow]{
	pending: "embedded = 0",
	missing: func(i Index) string { return "rid NOT IN (SELECT rowid FROM " + i.NotionTable() + ")" },
	query: func(where string) string {
		return "SELECT rid, title, path, markdown, private FROM notion_pages WHERE " + where + " AND in_trash = 0 ORDER BY rid LIMIT ?"
	},
	scan: func(r *sql.Rows) (notionRow, error) {
		var p notionRow
		err := r.Scan(&p.rid, &p.title, &p.path, &p.markdown, &p.private)
		return p, err
	},
	document: func(ctx context.Context, p notionRow) string {
		return NotionDocumentText(ctx, p.title, p.path, p.markdown)
	},
	table:  func(i Index) string { return i.NotionTable() },
	insert: "INSERT INTO %s (rowid, visibility, embedding) VALUES (?, ?, ?)",
	mark:   "UPDATE notion_pages SET embedded = 1 WHERE rid = ?",
	id:     func(p notionRow) int64 { return p.rid },
	partition: func(p notionRow) string {
		if p.private != 0 {
			return VisibilityPrivate
		}
		return VisibilityPublic
	},
}

func textsOr(texts TextsFunc) TextsFunc {
	if texts == nil {
		return Texts
	}
	return texts
}

func fill[T any](ctx context.Context, d *db.DB, url string, idx Index, src source[T], where string, mark bool, texts TextsFunc) (int, error) {
	rows, err := d.Read.QueryContext(ctx, src.query(where), endpoint.From(ctx).Batch)
	if err != nil {
		return 0, err
	}
	batch, err := collect(rows, src.scan)
	if err != nil || len(batch) == 0 {
		return 0, err
	}
	docs := make([]string, len(batch))
	for i, item := range batch {
		docs[i] = src.document(ctx, item)
	}
	vectors, err := textsOr(texts)(ctx, url, docs)
	if err != nil {
		return 0, err
	}
	table := src.table(idx)
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		for i, item := range batch {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE rowid = ?", src.id(item)); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(src.insert, table), src.id(item), src.partition(item), Serialize(vectors[i])); err != nil {
				return err
			}
			if !mark {
				continue
			}
			if _, err := tx.ExecContext(ctx, src.mark, src.id(item)); err != nil {
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

func drain[T any](ctx context.Context, d *db.DB, url string, src source[T], texts TextsFunc) (int, error) {
	done := 0
	for {
		idx, err := Active(ctx, d.Read)
		if err != nil {
			return done, err
		}
		n, err := fill(ctx, d, url, idx, src, src.pending, true, texts)
		done += n
		if err != nil || n == 0 {
			return done, err
		}
		log.Info(fmt.Sprintf("embedded %d", done))
	}
}

func Pending(ctx context.Context, d *db.DB, url string, texts TextsFunc) (int, error) {
	return drain(ctx, d, url, messageSource, texts)
}

func PendingOrWarn(ctx context.Context, d *db.DB, url string, passLog *slog.Logger) error {
	_, err := Pending(ctx, d, url, nil)
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
	return Pending(ctx, d, url, nil)
}

func NotionPending(ctx context.Context, d *db.DB, url string, texts TextsFunc) (int, error) {
	return drain(ctx, d, url, notionSource, texts)
}

func Fill(ctx context.Context, d *db.DB, url string, idx Index, texts TextsFunc) (int, error) {
	n, err := fill(ctx, d, url, idx, messageSource, messageSource.missing(idx), false, texts)
	if err != nil || n > 0 {
		return n, err
	}
	return fill(ctx, d, url, idx, notionSource, notionSource.missing(idx), false, texts)
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

func EmbedURL(ctx context.Context, q endpoint.Querier) (string, error) {
	active, err := Active(ctx, q)
	if err != nil {
		return "", err
	}
	return endpoint.EmbedURL(ctx, q, active.URL)
}

func WithModels(ctx context.Context, q endpoint.Querier) (context.Context, error) {
	active, err := Active(ctx, q)
	if err != nil {
		return ctx, err
	}
	return endpoint.WithModels(ctx, q, endpoint.Active{URL: active.URL, Model: active.Model, Dim: active.Dimension})
}
