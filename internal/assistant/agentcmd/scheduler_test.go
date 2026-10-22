package agentcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/sdnotify/sdnotifytest"
)

func TestSchedulerRunsUntilCancelledAndExitsZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	env := config.Map(map[string]string{"ATRIUM_DB": path, "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"), "ATRIUM_MODE": "shadow"})
	notify := sdnotifytest.Listen(t, 2*time.Second)
	a := cli.New("atrium", func(io.Writer) {})
	a.Add(&cli.Command{Name: "agent", Commands: []*cli.Command{schedulerCommand(env, noTools, func(string) bool { return true })}})
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

func TestResumedRunSupersedesTheOriginalLoop(t *testing.T) {
	srv := anthropictest.New(t,
		anthropictest.Reply("tool_use", anthropictest.ToolUse("call-1", "count", nil)),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("call-1", "count", nil)),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	env := config.Map(map[string]string{
		"ATRIUM_DB": path, "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"), "ATRIUM_MODE": "shadow",
		"ANTHROPIC_API_KEY": anthropictest.APIKey, anthropic.BaseURLEnv: srv.URL,
	})
	var calls atomic.Int32
	tools := func() (*registry.Registry, error) {
		r := registry.New()
		err := r.Add(tool.Tool{
			Name: "count", Description: "Count invocations", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			Effect: tool.Write, Contexts: tool.Kinds{tool.Cron},
			Handler: func(context.Context, *tool.Context, py.Dict) (any, error) {
				calls.Add(1)
				return "counted", nil
			},
		})
		return r, err
	}
	arrived := make(chan struct{}, 3)
	release := make(chan struct{})
	var first sync.Once
	srv.OnRequest = func(anthropictest.Request) {
		hold := false
		first.Do(func() { hold = true })
		arrived <- struct{}{}
		if hold {
			<-release
		}
	}
	a := cli.New("atrium", func(io.Writer) {})
	a.Add(&cli.Command{Name: "agent", Commands: []*cli.Command{runCommand(env, tools), schedulerCommand(env, tools, func(string) bool { return true })}})
	var runOut, runErr bytes.Buffer
	runDone := make(chan int)
	go func() {
		runDone <- a.Run(t.Context(), []string{"agent", "run", "--text", "hello", "--kind", "cron"}, &runOut, &runErr)
	}()
	<-arrived
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec("UPDATE runs SET lease_until = 0 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	schedDone := make(chan int)
	var schedOut, schedErr bytes.Buffer
	go func() { schedDone <- a.Run(ctx, []string{"agent", "scheduler"}, &schedOut, &schedErr) }()
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-time.After(20 * time.Second):
			t.Fatal("the scheduler never resumed and completed the interrupted run")
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		var state string
		if err := d.Read.QueryRow("SELECT state FROM runs WHERE id = 1").Scan(&state); err == nil && state == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the resumed run never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(release)
	select {
	case code := <-runDone:
		if code != cli.ExitOK {
			t.Fatalf("exit %d: %s", code, runErr.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the original loop never stopped")
	}
	cancel()
	<-schedDone
	var assistant, spent int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM thread_messages WHERE role = 'assistant'").Scan(&assistant); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM usage WHERE run_id = 1").Scan(&spent); err != nil {
		t.Fatal(err)
	}
	if assistant != 2 {
		t.Errorf("%d assistant messages: expected resumed tool response and final response only", assistant)
	}
	if spent != 3 {
		t.Errorf("%d usage rows: all model calls must be accounted", spent)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("tool ran %d times; expected exactly once", got)
	}
}
