package embed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/db"
)

const (
	StateActive   = "active"
	StateBuilding = "building"
)

type Index struct {
	Generation int64  `json:"generation"`
	State      string `json:"state"`
	URL        string `json:"url"`
	Model      string `json:"model"`
	Dimension  int    `json:"dimension"`
	Done       int    `json:"done"`
	Total      int    `json:"total"`
	Error      string `json:"error"`
}

func (i Index) MessageTable() string { return i.Table("message") }

func (i Index) KBTable() string { return i.Table("kb") }

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func lookup(ctx context.Context, q rowQuerier, state string) (Index, bool, error) {
	var i Index
	err := q.QueryRowContext(ctx, "SELECT generation, state, url, model, dimension, done, total, error FROM embed_index WHERE state = ?", state).
		Scan(&i.Generation, &i.State, &i.URL, &i.Model, &i.Dimension, &i.Done, &i.Total, &i.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return Index{}, false, nil
	}
	return i, err == nil, err
}

func Active(ctx context.Context, q rowQuerier) (Index, error) {
	i, ok, err := lookup(ctx, q, StateActive)
	if err == nil && !ok {
		err = errors.New("embed: no active embedding index")
	}
	return i, err
}

func Building(ctx context.Context, d *db.DB, corpora []Corpus) (*Build, bool, error) {
	i, ok, err := lookup(ctx, d.Read, StateBuilding)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &Build{Index: i, d: d, corpora: corpora}, true, nil
}

func Indexes(ctx context.Context, q rowQuerier) ([]Index, error) {
	active, err := Active(ctx, q)
	if err != nil {
		return nil, err
	}
	building, ok, err := lookup(ctx, q, StateBuilding)
	if err != nil || !ok {
		return []Index{active}, err
	}
	return []Index{active, building}, nil
}

type Build struct {
	Index
	d       *db.DB
	corpora []Corpus
}

func (b *Build) building() error {
	if b.State != StateBuilding {
		return errors.New("embed: only a building index can be changed")
	}
	return nil
}

func (b *Build) Record(ctx context.Context, failure string) error {
	if err := b.building(); err != nil {
		return err
	}
	done, total, err := counts(ctx, b.d.Read, b.Index, b.corpora)
	if err != nil {
		return err
	}
	_, err = b.d.Write.ExecContext(ctx, "UPDATE embed_index SET done = ?, total = ?, error = ? WHERE generation = ?", done, total, failure, b.Generation)
	return err
}

func (b *Build) Swap(ctx context.Context) (bool, error) {
	if err := b.building(); err != nil {
		return false, err
	}
	swapped := false
	err := b.d.WithTx(ctx, func(tx *sql.Tx) error {
		done, total, err := counts(ctx, tx, b.Index, b.corpora)
		if err != nil || done < total {
			return err
		}
		old, err := Active(ctx, tx)
		if err != nil {
			return err
		}
		if err := drop(ctx, tx, old, b.corpora); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE embed_index SET state = ?, done = ?, total = ?, error = '' WHERE generation = ?",
			StateActive, done, total, b.Generation); err != nil {
			return err
		}
		swapped = true
		return nil
	})
	return swapped, err
}

func (b *Build) Discard(ctx context.Context) error {
	if err := b.building(); err != nil {
		return err
	}
	return b.d.WithTx(ctx, func(tx *sql.Tx) error {
		for _, c := range b.corpora {
			if c.clear == nil {
				continue
			}
			if err := c.clear(ctx, tx, b.Table(c.Name)); err != nil {
				return err
			}
		}
		return drop(ctx, tx, b.Index, b.corpora)
	})
}

func (i Index) Adopt(ctx context.Context, d *db.DB, url, model string) error {
	if i.URL != "" {
		return errors.New("embed: the active index already names an endpoint")
	}
	_, err := d.Write.ExecContext(ctx, "UPDATE embed_index SET url = ?, model = ? WHERE generation = ? AND url = ''", url, model, i.Generation)
	return err
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	rowQuerier
}

func DeleteKB(ctx context.Context, q execer, id int64) error {
	for _, state := range []string{StateActive, StateBuilding} {
		i, ok, err := lookup(ctx, q, state)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if _, err := q.ExecContext(ctx, "DELETE FROM "+i.KBTable()+" WHERE rowid = ?", id); err != nil {
			return err
		}
	}
	return nil
}

func DeleteMessages(ctx context.Context, q execer, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	marks := "(?" + strings.TrimSuffix(strings.Repeat(",?", len(ids)-1), ",") + ")"
	for _, state := range []string{StateActive, StateBuilding} {
		i, ok, err := lookup(ctx, q, state)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if _, err := q.ExecContext(ctx, "DELETE FROM "+i.MessageTable()+" WHERE rowid IN "+marks, args...); err != nil {
			return err
		}
	}
	return nil
}

func Create(ctx context.Context, d *db.DB, corpora []Corpus, url, model string, dim int) (*Build, error) {
	i := Index{State: StateBuilding, URL: url, Model: model, Dimension: dim}
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(generation), 0) + 1 FROM embed_index").Scan(&i.Generation); err != nil {
			return err
		}
		for _, c := range corpora {
			stmt := fmt.Sprintf("CREATE VIRTUAL TABLE %s USING vec0(%sembedding FLOAT[%d] DISTANCE_METRIC=cosine)", i.Table(c.Name), c.columns, dim)
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO embed_index (generation, state, url, model, dimension) VALUES (?, ?, ?, ?, ?)",
			i.Generation, i.State, url, model, dim)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &Build{Index: i, d: d, corpora: corpora}, nil
}

func drop(ctx context.Context, tx *sql.Tx, i Index, corpora []Corpus) error {
	for _, c := range corpora {
		if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS "+i.Table(c.Name)); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM embed_index WHERE generation = ?", i.Generation)
	return err
}

func counts(ctx context.Context, q db.Querier, i Index, corpora []Corpus) (done, total int, err error) {
	for _, c := range corpora {
		cDone, cTotal, err := c.counts(ctx, q, i.Table(c.Name))
		if err != nil {
			return 0, 0, err
		}
		done, total = done+cDone, total+cTotal
	}
	return done, total, nil
}
