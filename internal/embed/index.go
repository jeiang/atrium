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
	Generation int64  `json:"generation"`
	State      string `json:"state"`
	URL        string `json:"url"`
	Model      string `json:"model"`
	Dimension  int    `json:"dimension"`
	Done       int    `json:"done"`
	Total      int    `json:"total"`
	Error      string `json:"error"`
}

func (i Index) MessageTable() string {
	return fmt.Sprintf("embed_message_%d", i.Generation)
}

func (i Index) NotionTable() string {
	return fmt.Sprintf("embed_notion_%d", i.Generation)
}

func (i Index) KBTable() string {
	return fmt.Sprintf("embed_kb_%d", i.Generation)
}

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

func Building(ctx context.Context, d *db.DB) (*Build, bool, error) {
	i, ok, err := lookup(ctx, d.Read, StateBuilding)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &Build{Index: i, d: d}, true, nil
}

type Build struct {
	Index
	d *db.DB
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
	done, total, err := counts(ctx, b.d.Read, b.Index)
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
		done, total, err := counts(ctx, tx, b.Index)
		if err != nil || done < total {
			return err
		}
		old, err := Active(ctx, tx)
		if err != nil {
			return err
		}
		for _, stmt := range []string{
			"UPDATE messages SET embedded = 1 WHERE id IN (SELECT rowid FROM " + b.MessageTable() + ")",
			"UPDATE notion_pages SET embedded = 1 WHERE rid IN (SELECT rowid FROM " + b.NotionTable() + ")",
			"UPDATE kb_records SET embedded = 1 WHERE id IN (SELECT rowid FROM " + b.KBTable() + ")",
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		if err := drop(ctx, tx, old); err != nil {
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
	return b.d.WithTx(ctx, func(tx *sql.Tx) error { return drop(ctx, tx, b.Index) })
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

func DeleteNotion(ctx context.Context, q execer, rid int64) error {
	return deleteNotion(ctx, q, rid, StateActive, StateBuilding)
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

func Create(ctx context.Context, d *db.DB, url, model string, dim int) (*Build, error) {
	i := Index{State: StateBuilding, URL: url, Model: model, Dimension: dim}
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(generation), 0) + 1 FROM embed_index").Scan(&i.Generation); err != nil {
			return err
		}
		for _, stmt := range []string{
			fmt.Sprintf("CREATE VIRTUAL TABLE %s USING vec0(account TEXT PARTITION KEY, embedding FLOAT[%d] DISTANCE_METRIC=cosine)", i.MessageTable(), dim),
			fmt.Sprintf("CREATE VIRTUAL TABLE %s USING vec0(visibility TEXT PARTITION KEY, embedding FLOAT[%d] DISTANCE_METRIC=cosine)", i.NotionTable(), dim),
			fmt.Sprintf("CREATE VIRTUAL TABLE %s USING vec0(embedding FLOAT[%d] DISTANCE_METRIC=cosine)", i.KBTable(), dim),
		} {
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
	return &Build{Index: i, d: d}, nil
}

func drop(ctx context.Context, tx *sql.Tx, i Index) error {
	for _, stmt := range []string{"DROP TABLE IF EXISTS " + i.MessageTable(), "DROP TABLE IF EXISTS " + i.NotionTable(), "DROP TABLE IF EXISTS " + i.KBTable()} {
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
		"(SELECT COUNT(*) FROM notion_pages WHERE in_trash = 0 AND rid IN (SELECT rowid FROM "+i.NotionTable()+")) + "+
		"(SELECT COUNT(*) FROM kb_records WHERE retracted_at IS NULL AND scope != 'ops' AND id IN (SELECT rowid FROM "+i.KBTable()+")), "+
		"(SELECT COUNT(*) FROM messages WHERE gone_at IS NULL AND id IN (SELECT rowid FROM message_fts)) + "+
		"(SELECT COUNT(*) FROM notion_pages WHERE in_trash = 0) + "+
		"(SELECT COUNT(*) FROM kb_records WHERE retracted_at IS NULL AND scope != 'ops')").Scan(&done, &total)
	return done, total, err
}
