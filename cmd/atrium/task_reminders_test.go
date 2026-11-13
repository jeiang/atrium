package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func TestTaskRemindersDeliverOnceThroughTheScheduler(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	runAppContext := func(ctx context.Context, args ...string) appRun {
		var out bytes.Buffer
		var errs lockedBuffer
		code := f.app.Run(ctx, args, &out, &errs)
		return appRun{code, out.String(), errs.String()}
	}
	push, browser := pushRig(t, dir)
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db", db.Key{})
	base, _, stopServe := startServeStoppable(t, dir)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	for _, step := range []struct {
		path string
		form url.Values
	}{
		{"/push/subscriptions", browser.form()},
		{"/channels/assistant/content", url.Values{"content": {"full"}}},
	} {
		fetch(t, client, http.MethodGet, base+"/settings/notifications", nil)
		if code, body, _ := fetch(t, client, http.MethodPost, base+"/settings/notifications"+step.path, step.form); code != http.StatusOK {
			t.Fatalf("POST %s = %d %s", step.path, code, body)
		}
	}
	stopServe()
	now := float64(time.Now().Unix())
	tasks := 0
	insert := func(title string, remindAt float64, done bool) {
		t.Helper()
		tasks++
		api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse(fmt.Sprintf("add%d", tasks), "task_add", map[string]any{"title": title, "remind_at": remindAt})))
		if done {
			api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse(fmt.Sprintf("done%d", tasks), "task_done", map[string]any{"id": tasks})))
		}
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("added")))
		if ran := runApp(t, "agent", "run", "--kind", "chat", "--text", "add a task"); ran.code != cli.ExitOK {
			t.Fatalf("task run: %d %s", ran.code, ran.stderr)
		}
		var status string
		if err := d.Read.QueryRow("SELECT status FROM tasks WHERE id = ? AND title = ?", tasks, title).Scan(&status); err != nil || (status == "done") != done {
			t.Fatalf("task %q status = %q, %v", title, status, err)
		}
	}
	start := func() (context.CancelFunc, chan appRun) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan appRun, 1)
		go func() { done <- runAppContext(ctx, "serve", "--host", "127.0.0.1", "--port", "0", "--auth", "local") }()
		return cancel, done
	}
	cancel, done := start()
	for i := 0; i < 50; i++ {
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM jobs WHERE name = 'task-reminders'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	<-done
	due := func() {
		t.Helper()
		if _, err := d.Write.Exec("UPDATE jobs SET state = 'active', next_due = ?, lease_owner = NULL, lease_until = NULL WHERE name = 'task-reminders'", now-1); err != nil {
			t.Fatal(err)
		}
	}
	runOnce := func() map[string]any {
		t.Helper()
		cancel, done := start()
		select {
		case p := <-push.arrived:
			time.Sleep(300 * time.Millisecond)
			cancel()
			<-done
			return browser.decrypt(t, p.body)
		case <-time.After(5 * time.Second):
			cancel()
			ran := <-done
			t.Fatalf("no reminder was posted: exit %d stderr %s", ran.code, ran.stderr)
			return nil
		}
	}
	runNone := func() {
		t.Helper()
		cancel, done := start()
		select {
		case p := <-push.arrived:
			cancel()
			<-done
			t.Fatalf("unexpected reminder: %v", browser.decrypt(t, p.body))
		case <-time.After(2 * time.Second):
		}
		cancel()
		<-done
	}
	insert("Pay the rent", now-1, false)
	due()
	if p := runOnce(); !strings.Contains(fmt.Sprint(p["title"]), "Pay the rent") {
		t.Fatalf("reminder payload = %v", p)
	}
	due()
	runNone()
	insert("Done task", now-1, true)
	insert("Future task", now+3600, false)
	due()
	runNone()
}
