package agentcmd

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/sdnotify/sdnotifytest"
)

func TestSchedulerRunsUntilCancelledAndExitsZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	env := config.Map(map[string]string{"ATRIUM_DB": path, "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"), "ATRIUM_MODE": "shadow"})
	notify := sdnotifytest.Listen(t, 2*time.Second)
	a := cli.New("atrium", func(io.Writer) {})
	a.Add(&cli.Command{Name: "agent", Commands: []*cli.Command{schedulerCommand(env, registry.New, func(string) bool { return true })}})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int)
	var stdout, stderr bytes.Buffer
	go func() { done <- a.Run(ctx, []string{"agent", "scheduler"}, &stdout, &stderr) }()
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		err := d.Read.QueryRow("SELECT COUNT(*) FROM jobs WHERE kind = 'nightly'").Scan(&n)
		if err == nil && n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the first tick never seeded the nightly job: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	notify.Wait("WATCHDOG=1")
	notify.Wait("READY=1")
	notify.Wait("STATUS=scheduling")
	cancel()
	notify.Wait("STOPPING=1")
	select {
	case code := <-done:
		if code != cli.ExitOK {
			t.Fatalf("exit %d: %s", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the scheduler ignored cancellation")
	}
}
