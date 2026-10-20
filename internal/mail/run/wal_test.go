package run

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

// alwaysOnReader is the web's reader pool over the same file, as a serve process that never stops answering
// pages: n goroutines run the triage, message and proposals queries until stop is called, each pausing a
// millisecond between queries. Back to back they starve the pass of CPU (the pure-Go driver under -race took
// minutes), and a reader that is idle for a millisecond still holds a read snapshot open most of the time.
const readPause = time.Millisecond

func alwaysOnReader(t *testing.T, path string, n int) (reads *atomic.Int64, stop func()) {
	t.Helper()
	r, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	reads = &atomic.Int64{}
	var wg sync.WaitGroup
	queries := []string{
		"SELECT m.id, m.subject, d.stage, d.dest FROM messages m LEFT JOIN decisions d ON d.message_id = m.id " +
			"WHERE m.gone_at IS NULL ORDER BY m.id DESC LIMIT 50",
		"SELECT id, kind, status, title FROM proposals ORDER BY created_ts DESC, id DESC LIMIT 50",
		"SELECT subject, sender, body FROM message_fts WHERE rowid = (SELECT MAX(id) FROM messages)",
		"SELECT COUNT(*) FROM auto_actions",
	}
	for i := range n {
		wg.Go(func() {
			for k := i; ctx.Err() == nil; k++ {
				rows, err := r.Read.QueryContext(ctx, queries[k%len(queries)])
				if err != nil {
					if ctx.Err() == nil {
						t.Error(err)
					}
					return
				}
				for rows.Next() {
				}
				_ = rows.Close()
				reads.Add(1)
				time.Sleep(readPause)
			}
		})
	}
	return reads, func() {
		cancel()
		wg.Wait()
		_ = r.Close()
	}
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// TestWALStaysBoundedUnderAnAlwaysOnReader runs passes that keep ingesting mail while the web's reader pool
// never stops querying: the end-of-pass TRUNCATE checkpoint must keep the WAL from growing across passes, so
// after every pass it is empty again (its peak within a pass is one pass's frames), and the share of
// checkpoints a reader blocks is reported.
func TestWALStaysBoundedUnderAnAlwaysOnReader(t *testing.T) {
	const passes, perPass = 6, 25
	w := newWorld(t, config.Shadow, credentials)
	logs := captureLogs(t)
	reads, stop := alwaysOnReader(t, w.cfg.DBPath, 4)
	defer stop()
	wal := w.cfg.DBPath + "-wal"
	var peak atomic.Int64
	sampling, stopSampling := context.WithCancel(context.Background())
	var sampler sync.WaitGroup
	sampler.Go(func() {
		for sampling.Err() == nil {
			peak.Store(max(peak.Load(), fileSize(wal)))
			time.Sleep(time.Millisecond)
		}
	})
	var afterPass []int64
	ctx := context.Background()
	for i := range passes {
		for j := range perPass {
			w.arrive(config.ICloud, fmt.Sprintf("Pass %d mail %d", i, j), fmt.Sprintf("s%d@x.test", j))
		}
		if err := w.pass.Locked(ctx, ctx, []string{config.ICloud}); err != nil {
			t.Fatal(err)
		}
		afterPass = append(afterPass, fileSize(wal))
	}
	stopSampling()
	sampler.Wait()
	stop()
	busy := strings.Count(logs.String(), "wal checkpoint blocked by a reader")
	c, err := w.db.Checkpoint(ctx, time.Second)
	if err != nil || c.Busy {
		t.Fatalf("idle checkpoint after the reader stopped = %+v, %v", c, err)
	}
	t.Logf("%d passes of %d messages under %d reads: WAL peak within a pass %d bytes, after each pass %v, %d of %d checkpoints blocked, %d bytes after an idle checkpoint",
		passes, perPass, reads.Load(), peak.Load(), afterPass, busy, passes, fileSize(wal))
	if n := w.count("SELECT COUNT(*) FROM messages"); n != passes*perPass {
		t.Errorf("messages = %d", n)
	}
	if busy == passes {
		t.Errorf("every checkpoint was blocked")
	}
	for i, size := range afterPass {
		if size > peak.Load()/2 {
			t.Errorf("WAL after pass %d holds %d bytes of the %d a pass reaches: the checkpoint did not truncate it", i, size, peak.Load())
		}
	}
	if fileSize(wal) != 0 {
		t.Errorf("WAL holds %d bytes after an idle TRUNCATE checkpoint", fileSize(wal))
	}
}
