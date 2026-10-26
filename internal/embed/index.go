package embed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/db"
)

const (
	StateActive   = "active"
	StateBuilding = "building"
)

type Index struct {
	Generation int64   `json:"generation"`
	State      string  `json:"state"`
	URL        string  `json:"url"`
	Model      string  `json:"model"`
	Dimension  int     `json:"dimension"`
	Done       int     `json:"done"`
	Total      int     `json:"total"`
	Error      string  `json:"error"`
	StartedAt  float64 `json:"started_at"`
	FinishedAt float64 `json:"finished_at"`
}

func (i Index) MessageTable() string {
	if i.Generation == 0 {
		return "message_vec"
	}
	return fmt.Sprintf("embed_message_%d", i.Generation)
}

func (i Index) NotionTable() string {
	if i.Generation == 0 {
		return "notion_vec"
	}
	return fmt.Sprintf("embed_notion_%d", i.Generation)
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func lookup(ctx context.Context, q rowQuerier, state string) (Index, bool, error) {
	var i Index
	var started, finished sql.NullFloat64
	err := q.QueryRowContext(ctx, "SELECT generation, state, url, model, dimension, done, total, error, started_at, finished_at "+
		"FROM embed_index WHERE state = ?", state).
		Scan(&i.Generation, &i.State, &i.URL, &i.Model, &i.Dimension, &i.Done, &i.Total, &i.Error, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return Index{}, false, nil
	}
	i.StartedAt, i.FinishedAt = started.Float64, finished.Float64
	return i, err == nil, err
}

func Active(ctx context.Context, q rowQuerier) (Index, error) {
	i, ok, err := lookup(ctx, q, StateActive)
	if err == nil && !ok {
		err = errors.New("embed: no active embedding index")
	}
	return i, err
}

func Building(ctx context.Context, q rowQuerier) (Index, bool, error) {
	return lookup(ctx, q, StateBuilding)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	rowQuerier
}

func DeleteNotion(ctx context.Context, q execer, rid int64) error {
	return deleteNotion(ctx, q, rid, StateActive, StateBuilding)
}

func StaleNotion(ctx context.Context, q execer, rid int64) error {
	return deleteNotion(ctx, q, rid, StateBuilding)
}

func deleteNotion(ctx context.Context, q execer, rid int64, states ...string) error {
	for _, state := range states {
		i, ok, err := lookup(ctx, q, state)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if _, err := q.ExecContext(ctx, "DELETE FROM "+i.NotionTable()+" WHERE rowid = ?", rid); err != nil {
			return err
		}
	}
	return nil
}

func Adopt(ctx context.Context, d *db.DB, url, model string) error {
	_, err := d.Write.ExecContext(ctx, "UPDATE embed_index SET url = ?, model = ? WHERE state = ? AND url = ''", url, model, StateActive)
	return err
}

func Create(ctx context.Context, d *db.DB, url, model string, dim int, now float64) (Index, error) {
	i := Index{State: StateBuilding, URL: url, Model: model, Dimension: dim, StartedAt: now}
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(generation), 0) + 1 FROM embed_index").Scan(&i.Generation); err != nil {
			return err
		}
		for _, stmt := range []string{
			fmt.Sprintf("CREATE VIRTUAL TABLE %s USING vec0(account TEXT PARTITION KEY, embedding FLOAT[%d] DISTANCE_METRIC=cosine)", i.MessageTable(), dim),
			fmt.Sprintf("CREATE VIRTUAL TABLE %s USING vec0(visibility TEXT PARTITION KEY, embedding FLOAT[%d] DISTANCE_METRIC=cosine)", i.NotionTable(), dim),
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO embed_index (generation, state, url, model, dimension, started_at) VALUES (?, ?, ?, ?, ?, ?)",
			i.Generation, i.State, url, model, dim, now)
		return err
	})
	return i, err
}

func Discard(ctx context.Context, d *db.DB, i Index) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error { return drop(ctx, tx, i) })
}

func drop(ctx context.Context, tx *sql.Tx, i Index) error {
	for _, stmt := range []string{"DROP TABLE IF EXISTS " + i.MessageTable(), "DROP TABLE IF EXISTS " + i.NotionTable()} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM embed_index WHERE generation = ?", i.Generation)
	return err
}

func counts(ctx context.Context, q rowQuerier, i Index) (done, total int, err error) {
	err = q.QueryRowContext(ctx, "SELECT "+
		"(SELECT COUNT(*) FROM messages WHERE gone_at IS NULL AND id IN (SELECT rowid FROM message_fts) AND id IN (SELECT rowid FROM "+i.MessageTable()+")) + "+
		"(SELECT COUNT(*) FROM notion_pages WHERE in_trash = 0 AND rid IN (SELECT rowid FROM "+i.NotionTable()+")), "+
		"(SELECT COUNT(*) FROM messages WHERE gone_at IS NULL AND id IN (SELECT rowid FROM message_fts)) + "+
		"(SELECT COUNT(*) FROM notion_pages WHERE in_trash = 0)").Scan(&done, &total)
	return done, total, err
}

func Record(ctx context.Context, d *db.DB, i Index, failure string) error {
	done, total, err := counts(ctx, d.Read, i)
	if err != nil {
		return err
	}
	_, err = d.Write.ExecContext(ctx, "UPDATE embed_index SET done = ?, total = ?, error = ? WHERE generation = ?", done, total, failure, i.Generation)
	return err
}

func Swap(ctx context.Context, d *db.DB, i Index, now float64) (bool, error) {
	swapped := false
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		done, total, err := counts(ctx, tx, i)
		if err != nil || done < total {
			return err
		}
		old, err := Active(ctx, tx)
		if err != nil {
			return err
		}
		for _, stmt := range []string{
			"UPDATE messages SET embedded = 1 WHERE id IN (SELECT rowid FROM " + i.MessageTable() + ")",
			"UPDATE notion_pages SET embedded = 1 WHERE rid IN (SELECT rowid FROM " + i.NotionTable() + ")",
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		if err := drop(ctx, tx, old); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE embed_index SET state = ?, done = ?, total = ?, error = '', finished_at = ? WHERE generation = ?",
			StateActive, done, total, now, i.Generation); err != nil {
			return err
		}
		swapped = true
		return nil
	})
	return swapped, err
}
