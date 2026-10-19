package db_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/db"
)

func TestCheckpointTruncatesAndWaitsOnlyBriefly(t *testing.T) {
	ctx := context.Background()
	d, err := db.Connect(filepath.Join(t.TempDir(), "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	write := func() {
		t.Helper()
		if _, err := d.Write.Exec("CREATE TABLE IF NOT EXISTS t (x)"); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Write.Exec("INSERT INTO t VALUES (1)"); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if c, err := d.Checkpoint(ctx, time.Second); err != nil || c.Busy {
		t.Fatalf("idle checkpoint = %+v, %v", c, err)
	}

	write()
	reader, err := d.Read.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := reader.QueryRow("SELECT COUNT(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}
	write()
	start := time.Now()
	c, err := d.Checkpoint(ctx, 100*time.Millisecond)
	if err != nil || !c.Busy {
		t.Fatalf("checkpoint under a reader = %+v, %v", c, err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("checkpoint under a reader took %v", took)
	}
	_ = reader.Rollback()

	var busy int
	if err := d.Write.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil || busy != 30000 {
		t.Fatalf("writer busy_timeout after the checkpoint = %d, %v", busy, err)
	}
}
