//go:build !modernc

package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/db"
)

func TestDatabaseOpenTunesEveryConnectionByRole(t *testing.T) {
	k, err := db.ParseKey(rightKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Connect(filepath.Join(t.TempDir(), "atrium.db"), db.WithKey(k))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	ctx := context.Background()
	for _, tc := range []struct {
		role  string
		pool  *sql.DB
		conns int
		cache int64
	}{
		{"write", d.Write, 1, -2048},
		{"read", d.Read, 3, -2048},
		{"knn", d.KNN, 1, -196608},
	} {
		var held []*sql.Conn
		for range tc.conns {
			c, err := tc.pool.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			held = append(held, c)
		}
		for i, c := range held {
			for name, want := range map[string]int64{"cache_size": tc.cache, "temp_store": 2, "journal_size_limit": 67108864} {
				var got int64
				if err := c.QueryRowContext(ctx, "PRAGMA "+name).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Errorf("%s connection %d: %s = %d, want %d", tc.role, i, name, got, want)
				}
			}
			_ = c.Close()
		}
	}
}
