package embed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
)

type Source[T any] struct {
	Name       string
	Columns    string
	Insert     string
	Background bool
	Pending    string
	Compare    bool
	Missing    func(table string) string
	Load       func(context.Context, db.Querier, string, int, int64) ([]T, error)
	Reload     func(context.Context, db.Querier, int64) (T, bool, error)
	Document   func(context.Context, T) string
	Hash       func(T) string
	Mark       func(context.Context, db.Querier, int64, string) error
	ID         func(T) int64
	Partition  func(T) string
	Reset      func(context.Context, db.Querier) error
	Clear      func(context.Context, db.Querier, string) error
	Counts     func(context.Context, db.Querier, string) (done, total int, err error)
}

type Corpus struct {
	Name       string
	Background bool
	columns    string
	fill       func(ctx context.Context, d *db.DB, url string, idx Index, missing bool, texts TextsFunc) (int, error)
	reset      func(context.Context, db.Querier) error
	clear      func(context.Context, db.Querier, string) error
	counts     func(context.Context, db.Querier, string) (int, int, error)
}

func NewCorpus[T any](src Source[T]) Corpus {
	return Corpus{
		Name:       src.Name,
		Background: src.Background,
		columns:    src.Columns,
		fill: func(ctx context.Context, d *db.DB, url string, idx Index, missing bool, texts TextsFunc) (int, error) {
			if missing {
				return fill(ctx, d, url, idx, src, src.Missing(idx.Table(src.Name)), false, texts)
			}
			return fill(ctx, d, url, idx, src, src.Pending, src.Compare, texts)
		},
		reset:  src.Reset,
		clear:  src.Clear,
		counts: src.Counts,
	}
}

func (i Index) Table(corpus string) string {
	return fmt.Sprintf("embed_%s_%d", corpus, i.Generation)
}

func textsOr(texts TextsFunc) TextsFunc {
	if texts == nil {
		return Texts
	}
	return texts
}

func fill[T any](ctx context.Context, d *db.DB, url string, idx Index, src Source[T], where string, compare bool, texts TextsFunc) (int, error) {
	limit := endpoint.From(ctx).Batch
	var batch []T
	var docs, hashes []string
	var after int64
	for len(batch) < limit {
		want := limit - len(batch)
		rows, err := src.Load(ctx, d.Read, where, want, after)
		if err != nil {
			return 0, err
		}
		if len(rows) == 0 {
			break
		}
		for _, item := range rows {
			after = src.ID(item)
			doc := src.Document(ctx, item)
			hash := Hash(doc)
			if compare && src.Hash(item) == hash {
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
	table := idx.Table(src.Name)
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		for i, item := range batch {
			id := src.ID(item)
			current, ok, err := src.Reload(ctx, tx, id)
			if err != nil {
				return err
			}
			if !ok || Hash(src.Document(ctx, current)) != hashes[i] {
				if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE rowid = ?", id); err != nil {
					return err
				}
				continue
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE rowid = ?", id); err != nil {
				return err
			}
			args := []any{id}
			if src.Partition != nil {
				args = append(args, src.Partition(current))
			}
			args = append(args, Serialize(vectors[i]))
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(src.Insert, table), args...); err != nil {
				return err
			}
			if err := src.Mark(ctx, tx, id, hashes[i]); err != nil {
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

func Drain(ctx context.Context, d *db.DB, url string, c Corpus, texts TextsFunc) (int, error) {
	done := 0
	for {
		idx, err := Active(ctx, d.Read)
		if err != nil {
			return done, err
		}
		n, err := c.fill(ctx, d, url, idx, false, texts)
		done += n
		if err != nil || n == 0 {
			return done, err
		}
		log.Info(fmt.Sprintf("embedded %d", done))
	}
}

func DrainOrWarn(ctx context.Context, d *db.DB, url string, c Corpus, passLog *slog.Logger) error {
	_, err := Drain(ctx, d, url, c, nil)
	var unavailable *httpx.ServiceUnavailable
	if errors.As(err, &unavailable) {
		passLog.Warn(fmt.Sprintf("embedding unavailable: %s", unavailable))
		return nil
	}
	return err
}

func Reindex(ctx context.Context, d *db.DB, url string, c Corpus) (int, error) {
	if c.reset == nil {
		return 0, fmt.Errorf("embed: corpus %s cannot be reindexed", c.Name)
	}
	idx, err := Active(ctx, d.Read)
	if err != nil {
		return 0, err
	}
	if _, err := d.Write.ExecContext(ctx, "DELETE FROM "+idx.Table(c.Name)); err != nil {
		return 0, err
	}
	if err := c.reset(ctx, d.Write); err != nil {
		return 0, err
	}
	return Drain(ctx, d, url, c, nil)
}

func Fill(ctx context.Context, d *db.DB, corpora []Corpus, url string, idx Index, texts TextsFunc) (int, error) {
	for _, c := range corpora {
		n, err := c.fill(ctx, d, url, idx, true, texts)
		if err != nil || n > 0 {
			return n, err
		}
	}
	return 0, nil
}

func Reembed(ctx context.Context, d *db.DB, corpora []Corpus) (bool, error) {
	e, err := endpoint.OpenEmbedding(ctx, d.Read)
	if err != nil {
		if endpoint.IsUnconfigured(err) {
			return false, nil
		}
		return false, err
	}
	defer e.Close()
	ctx = endpoint.WithEmbedding(ctx, e)
	s := endpoint.Settings{EmbedBodyChars: endpoint.TuningMailEmbedBodyChars, Batch: endpoint.TuningMailEmbedBatch}
	return Rebuild(ctx, d, corpora, e.Base, e.Model, s, func(ctx context.Context, _, _ string) (int, error) {
		return e.Probe(ctx)
	})
}
