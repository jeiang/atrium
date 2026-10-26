package embed

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/py"
)

const (
	Model  = "embeddinggemma"
	KNNMax = 4096

	VisibilityPublic  = "public"
	VisibilityPrivate = "private"
)

var log = logx.Logger("atrium.embed")

type TextsFunc func(ctx context.Context, url string, texts []string) ([][]float64, error)

type Options struct {
	Model     string
	BodyChars int
	Batch     int
	Dim       int
}

type optionsKey struct{}

func WithOptions(ctx context.Context, o Options) context.Context {
	return context.WithValue(ctx, optionsKey{}, o)
}

func options(ctx context.Context) Options {
	o, _ := ctx.Value(optionsKey{}).(Options)
	if o.Model == "" {
		o.Model = Model
	}
	if o.BodyChars <= 0 {
		o.BodyChars = config.EmbedBodyChars
	}
	if o.Batch <= 0 {
		o.Batch = config.EmbedBatch
	}
	if o.Dim <= 0 {
		o.Dim = config.EmbedDim
	}
	return o
}

func DocumentText(ctx context.Context, subject, name, addr, body string) string {
	if subject == "" {
		subject = "none"
	}
	return "title: " + subject + " | text: From: " + name + " <" + addr + ">\n\n" + py.Clip(body, options(ctx).BodyChars)
}

func NotionDocumentText(title, path, markdown string) string {
	if title == "" {
		title = "none"
	}
	return "title: " + title + " | text: " + path + "\n\n" + py.Clip(markdown, config.EmbedBodyChars)
}

func QueryText(query string) string {
	return "task: search result | query: " + query
}

type embeddingsRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

func Texts(ctx context.Context, url string, texts []string) ([][]float64, error) {
	raw, err := httpx.PostJSON(ctx, url+"/embeddings", embeddingsRequest{options(ctx).Model, texts})
	if err != nil {
		return nil, err
	}
	var reply any
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("%w: %w", httpx.ErrValue, err)
	}
	data, err := httpx.Key(reply, "data")
	if err != nil {
		return nil, err
	}
	items, ok := data.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: embeddings data is %s, not list", httpx.ErrType, httpx.TypeName(data))
	}
	type entry struct {
		index float64
		item  any
	}
	entries := make([]entry, len(items))
	for i, item := range items {
		d, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: '%s' object has no attribute 'get'", httpx.ErrType, httpx.TypeName(item))
		}
		entries[i].item = item
		if idx, ok := d["index"]; ok {
			n, ok := idx.(float64)
			if !ok {
				return nil, fmt.Errorf("%w: embeddings index is %s", httpx.ErrType, httpx.TypeName(idx))
			}
			entries[i].index = n
		}
	}
	slices.SortStableFunc(entries, func(a, b entry) int { return cmp.Compare(a.index, b.index) })
	vectors := make([]any, len(entries))
	for i, e := range entries {
		if vectors[i], err = httpx.Key(e.item, "embedding"); err != nil {
			return nil, err
		}
	}
	dim := options(ctx).Dim
	shape := fmt.Errorf("%w: embedding response shape mismatch: expected %dx%d", httpx.ErrValue, len(texts), dim)
	if len(vectors) != len(texts) {
		return nil, shape
	}
	for _, v := range vectors {
		n, err := pyLen(v)
		if err != nil {
			return nil, err
		}
		if n != dim {
			return nil, shape
		}
	}
	out := make([][]float64, len(vectors))
	for i, v := range vectors {
		list, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("%w: embedding is %s, not list", httpx.ErrType, httpx.TypeName(v))
		}
		vec := make([]float64, len(list))
		for j, x := range list {
			if vec[j], err = httpx.Float(x); err != nil {
				return nil, err
			}
		}
		out[i] = Normalize(vec)
	}
	return out, nil
}

func pyLen(v any) (int, error) {
	switch t := v.(type) {
	case []any:
		return len(t), nil
	case map[string]any:
		return len(t), nil
	case string:
		return len([]rune(t)), nil
	}
	return 0, fmt.Errorf("%w: object of type '%s' has no len()", httpx.ErrType, httpx.TypeName(v))
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
		return "", fmt.Errorf("%w: message %d has no index row: 'NoneType' object is not subscriptable", httpx.ErrType, id)
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
		"SELECT id, account FROM messages WHERE "+where+" AND gone_at IS NULL ORDER BY id LIMIT ?", options(ctx).Batch)
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
		options(ctx).Batch)
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
		docs[i] = NotionDocumentText(p.title, p.path, p.markdown)
	}
	vectors, err := texts(ctx, url, docs)
	if err != nil {
		return 0, err
	}
	if len(vectors) != len(batch) {
		return 0, fmt.Errorf("%w: zip() argument 2 is shorter or longer than argument 1", httpx.ErrValue)
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
