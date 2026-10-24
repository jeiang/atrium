package db

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
)

type VirtualTable struct {
	Name string
	Kind string
	Rows int64
}

func quoteIdent(name string) string { return "\"" + strings.ReplaceAll(name, "\"", "\"\"") + "\"" }

func IntegrityCheck(ctx context.Context, q Querier) error {
	rows, err := q.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	lines, err := Collect(rows, func(r *sql.Rows) (string, error) {
		var l string
		err := r.Scan(&l)
		return l, err
	})
	if err != nil {
		return err
	}
	if len(lines) != 1 || lines[0] != "ok" {
		return fmt.Errorf("integrity_check: %s", strings.Join(lines, "; "))
	}
	return nil
}

func TableCounts(ctx context.Context, q Querier) (map[string]int64, error) {
	rows, err := q.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return nil, err
	}
	names, err := Collect(rows, func(r *sql.Rows) (string, error) {
		var n string
		err := r.Scan(&n)
		return n, err
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(names))
	for _, n := range names {
		var c int64
		if err := q.QueryRowContext(ctx, "SELECT count(*) FROM "+quoteIdent(n)).Scan(&c); err != nil {
			return nil, fmt.Errorf("count %s: %w", n, err)
		}
		out[n] = c
	}
	return out, nil
}

func VirtualTables(ctx context.Context, q Querier) ([]VirtualTable, error) {
	rows, err := q.QueryContext(ctx, "SELECT name, COALESCE(sql, '') FROM sqlite_master WHERE type = 'table' AND sql LIKE 'CREATE VIRTUAL TABLE%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	tables, err := Collect(rows, func(r *sql.Rows) (VirtualTable, error) {
		var name, text string
		if err := r.Scan(&name, &text); err != nil {
			return VirtualTable{}, err
		}
		upper := strings.ToUpper(text)
		switch {
		case strings.Contains(upper, "USING FTS5"):
			return VirtualTable{Name: name, Kind: "fts5"}, nil
		case strings.Contains(upper, "USING VEC0"):
			return VirtualTable{Name: name, Kind: "vec0"}, nil
		}
		return VirtualTable{}, nil
	})
	if err != nil {
		return nil, err
	}
	out := tables[:0]
	for _, t := range tables {
		if t.Kind == "" {
			continue
		}
		if err := q.QueryRowContext(ctx, "SELECT count(*) FROM "+quoteIdent(t.Name)).Scan(&t.Rows); err != nil {
			return nil, fmt.Errorf("count %s: %w", t.Name, err)
		}
		out = append(out, t)
	}
	return out, nil
}

func SpotCheckKNN(ctx context.Context, q Querier, t VirtualTable) error {
	if t.Kind != "vec0" || t.Rows == 0 {
		return nil
	}
	rows, err := q.QueryContext(ctx, "PRAGMA table_info("+quoteIdent(t.Name)+")")
	if err != nil {
		return err
	}
	columns, err := Collect(rows, func(r *sql.Rows) (string, error) {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		err := r.Scan(&cid, &name, &typ, &notnull, &dflt, &pk)
		if err != nil || !strings.HasPrefix(strings.ToLower(typ), "float") {
			return "", err
		}
		return name, nil
	})
	if err != nil {
		return err
	}
	column := ""
	for _, c := range columns {
		if c != "" {
			column = c
			break
		}
	}
	if column == "" {
		return nil
	}
	var rowid int64
	var vector []byte
	if err := q.QueryRowContext(ctx, "SELECT rowid, "+quoteIdent(column)+" FROM "+quoteIdent(t.Name)+" LIMIT 1").Scan(&rowid, &vector); err != nil {
		return fmt.Errorf("knn %s: %w", t.Name, err)
	}
	var found int64
	var distance float64
	if err := q.QueryRowContext(ctx, "SELECT rowid, distance FROM "+quoteIdent(t.Name)+" WHERE "+quoteIdent(column)+" MATCH ? AND k = 1", vector).Scan(&found, &distance); err != nil {
		return fmt.Errorf("knn %s: %w", t.Name, err)
	}
	if math.Abs(distance) > 1e-6 {
		return fmt.Errorf("knn %s: nearest neighbour of row %d is row %d at distance %g", t.Name, rowid, found, distance)
	}
	return nil
}

func SchemaVersion(ctx context.Context, q Querier) (int, error) {
	applied, err := appliedVersions(ctx, q)
	if err != nil {
		return 0, err
	}
	newest := 0
	for v := range applied {
		newest = max(newest, v)
	}
	return newest, nil
}

func LatestSchemaVersion() int { return migrations[len(migrations)-1].version }

func RetiredKeyIDs(ctx context.Context, q Querier) ([]string, error) {
	var n int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'db_key_history'").Scan(&n); err != nil || n == 0 {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, "SELECT key_id FROM db_key_history ORDER BY id")
	if err != nil {
		return nil, err
	}
	return Collect(rows, func(r *sql.Rows) (string, error) {
		var id string
		err := r.Scan(&id)
		return id, err
	})
}
