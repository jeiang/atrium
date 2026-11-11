package embed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
	kbembedding "github.com/jeiang/atrium/internal/kb/embedding"
	"github.com/jeiang/atrium/internal/logx"
	mailembedding "github.com/jeiang/atrium/internal/mail/embedding"
	notionembedding "github.com/jeiang/atrium/internal/notion/embedding"
	"github.com/jeiang/atrium/internal/obs"
	"github.com/jeiang/atrium/internal/textutil"
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
	return "title: " + subject + " | text: From: " + from + "\n\n" + textutil.Clip(body, endpoint.From(ctx).EmbedBodyChars)
}

func NotionDocumentText(ctx context.Context, title, path, markdown string) string {
	if title == "" {
		title = "none"
	}
	return "title: " + title + " | text: " + path + "\n\n" + textutil.Clip(markdown, endpoint.From(ctx).EmbedBodyChars)
}

func KBDocumentText(ctx context.Context, title, aliases, body string) string {
	if title == "" {
		title = "none"
	}
	return "title: " + title + " | text: " + aliases + "\n\n" + textutil.Clip(body, endpoint.From(ctx).EmbedBodyChars)
}

func QueryText(query string) string {
	return "task: search result | query: " + query
}

func Hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
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
	ctx, span := obs.Start(ctx, "embed.request", trace.WithAttributes(
		obs.String(obs.EmbedModel, s.EmbedModel),
		obs.Int(obs.EmbedBatchSize, len(texts)),
		obs.Int(obs.EmbedDimensions, s.Dim),
	))
	started := time.Now()
	vectors, err := textsAt(ctx, url, texts, s)
	duration := time.Since(started)
	span.SetAttributes(obs.Bool(obs.EmbedOK, err == nil))
	if err != nil {
		obs.Fail(span, "error")
	}
	span.End()
	obs.EmbedRequests.Add(1, s.EmbedModel, strconv.FormatBool(err == nil))
	obs.EmbedDuration.Observe(duration.Seconds(), s.EmbedModel)
	return vectors, err
}

func textsAt(ctx context.Context, url string, texts []string, s endpoint.Settings) ([][]float64, error) {
	embedding := endpoint.EmbeddingFrom(ctx)
	if embedding == nil {
		return nil, &httpx.ServiceUnavailable{Msg: "no embedding connection is configured"}
	}
	raw, err := embedding.Post(ctx, url, embeddingsRequest{s.EmbedModel, texts})
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
	var sum float64
	for _, x := range vector {
		sum += x * x
	}
	norm := math.Sqrt(sum)
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
	compare   bool
	missing   func(Index) string
	load      func(context.Context, db.Querier, string, int, int64) ([]T, error)
	reload    func(context.Context, db.Querier, int64) (T, bool, error)
	document  func(context.Context, T) string
	hash      func(T) string
	table     func(Index) string
	insert    string
	mark      func(context.Context, db.Querier, int64, string) error
	id        func(T) int64
	partition func(T) string
}

var messageSource = source[mailembedding.Message]{
	pending: "m.embedded_hash IS NULL",
	missing: func(i Index) string { return "m.id NOT IN (SELECT rowid FROM " + i.MessageTable() + ")" },
	load:    mailembedding.Pending,
	reload:  mailembedding.Load,
	document: func(ctx context.Context, m mailembedding.Message) string {
		return DocumentText(ctx, m.Subject, m.Name, m.Addr, m.Body)
	},
	hash:   func(m mailembedding.Message) string { return m.EmbeddedHash },
	table:  func(i Index) string { return i.MessageTable() },
	insert: "INSERT INTO %s (rowid, account, embedding) VALUES (?, ?, ?)",
	mark:   mailembedding.Mark,
	id:     func(m mailembedding.Message) int64 { return m.ID },
	partition: func(m mailembedding.Message) string {
		return m.Account
	},
}

var notionSource = source[notionembedding.Page]{
	compare: true,
	missing: func(i Index) string { return "rid NOT IN (SELECT rowid FROM " + i.NotionTable() + ")" },
	load:    notionembedding.Pending,
	reload:  notionembedding.Load,
	document: func(ctx context.Context, p notionembedding.Page) string {
		return NotionDocumentText(ctx, p.Title, p.Path, p.Markdown)
	},
	hash:   func(p notionembedding.Page) string { return p.EmbeddedHash },
	table:  func(i Index) string { return i.NotionTable() },
	insert: "INSERT INTO %s (rowid, visibility, embedding) VALUES (?, ?, ?)",
	mark:   notionembedding.Mark,
	id:     func(p notionembedding.Page) int64 { return p.RID },
	partition: func(p notionembedding.Page) string {
		if p.Private != 0 {
			return VisibilityPrivate
		}
		return VisibilityPublic
	},
}

var kbSource = source[kbembedding.Record]{
	compare: true,
	missing: func(i Index) string { return "id NOT IN (SELECT rowid FROM " + i.KBTable() + ")" },
	load:    kbembedding.Pending,
	reload:  kbembedding.Load,
	document: func(ctx context.Context, k kbembedding.Record) string {
		return KBDocumentText(ctx, k.Title, k.Aliases, k.Body)
	},
	hash:   func(k kbembedding.Record) string { return k.EmbeddedHash },
	table:  func(i Index) string { return i.KBTable() },
	insert: "INSERT INTO %s (rowid, embedding) VALUES (?, ?)",
	mark:   kbembedding.Mark,
	id:     func(k kbembedding.Record) int64 { return k.ID },
}

func textsOr(texts TextsFunc) TextsFunc {
	if texts == nil {
		return Texts
	}
	return texts
}

func fill[T any](ctx context.Context, d *db.DB, url string, idx Index, src source[T], where string, compare bool, texts TextsFunc) (int, error) {
	limit := endpoint.From(ctx).Batch
	var batch []T
	var docs, hashes []string
	var after int64
	for len(batch) < limit {
		want := limit - len(batch)
		rows, err := src.load(ctx, d.Read, where, want, after)
		if err != nil {
			return 0, err
		}
		if len(rows) == 0 {
			break
		}
		for _, item := range rows {
			after = src.id(item)
			doc := src.document(ctx, item)
			hash := Hash(doc)
			if compare && src.hash(item) == hash {
				continue
			}
			batch = append(batch, item)
			docs = append(docs, doc)
			hashes = append(hashes, hash)
		}
		if len(rows) < want {
			break
		}
	}
	if len(batch) == 0 {
		return 0, nil
	}
	vectors, err := textsOr(texts)(ctx, url, docs)
	if err != nil {
		return 0, err
	}
	table := src.table(idx)
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		for i, item := range batch {
			id := src.id(item)
			current, ok, err := src.reload(ctx, tx, id)
			if err != nil {
				return err
			}
			if !ok || Hash(src.document(ctx, current)) != hashes[i] {
				if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE rowid = ?", id); err != nil {
					return err
				}
				continue
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE rowid = ?", id); err != nil {
				return err
			}
			args := []any{id}
			if src.partition != nil {
				args = append(args, src.partition(current))
			}
			args = append(args, Serialize(vectors[i]))
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(src.insert, table), args...); err != nil {
				return err
			}
			if err := src.mark(ctx, tx, id, hashes[i]); err != nil {
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
		n, err := fill(ctx, d, url, idx, src, src.pending, src.compare, texts)
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
	if err := mailembedding.Reset(ctx, d.Write); err != nil {
		return 0, err
	}
	return Pending(ctx, d, url, nil)
}

func NotionPending(ctx context.Context, d *db.DB, url string, texts TextsFunc) (int, error) {
	return drain(ctx, d, url, notionSource, texts)
}

func KBPending(ctx context.Context, d *db.DB, url string, texts TextsFunc) (int, error) {
	return drain(ctx, d, url, kbSource, texts)
}

func Fill(ctx context.Context, d *db.DB, url string, idx Index, texts TextsFunc) (int, error) {
	n, err := fill(ctx, d, url, idx, messageSource, messageSource.missing(idx), false, texts)
	if err != nil || n > 0 {
		return n, err
	}
	n, err = fill(ctx, d, url, idx, notionSource, notionSource.missing(idx), false, texts)
	if err != nil || n > 0 {
		return n, err
	}
	return fill(ctx, d, url, idx, kbSource, kbSource.missing(idx), false, texts)
}

func EmbedURL(ctx context.Context, q endpoint.Querier) (string, error) {
	active, err := Active(ctx, q)
	if err != nil {
		return "", err
	}
	return EmbedURLFor(ctx, q, active)
}

func EmbedURLFor(ctx context.Context, q endpoint.Querier, active Index) (string, error) {
	return endpoint.EmbedURL(ctx, q, active.URL)
}

func WithModels(ctx context.Context, q endpoint.Querier) (context.Context, error) {
	active, err := Active(ctx, q)
	if err != nil {
		return ctx, err
	}
	return WithIndex(ctx, q, active)
}

func WithIndex(ctx context.Context, q endpoint.Querier, active Index) (context.Context, error) {
	return endpoint.WithModels(ctx, q, endpoint.Active{URL: active.URL, Model: active.Model, Dim: active.Dimension})
}
