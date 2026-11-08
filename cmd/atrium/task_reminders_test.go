package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/settings"
)

type reminderPost struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}

func runAppContext(ctx context.Context, args ...string) appRun {
	env := config.Env(testLookup)
	if activeFixture != nil {
		env = activeFixture.lookup
	}
	var out bytes.Buffer
	var errs lockedBuffer
	code := newApp(env, settings.Default, nil).Run(ctx, args, &out, &errs)
	return appRun{code, out.String(), errs.String()}
}

func TestTaskRemindersDeliverOnceThroughTheScheduler(t *testing.T) {
	dir, _ := moduleFixture(t, flagsExcept("", ""))
	posts := make(chan reminderPost, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p reminderPost
		if err := json.NewDecoder(r.Body).Decode(&p); err == nil {
			posts <- p
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	setEnv(t, settingEnv(notify.KeyURL), srv.URL)
	if ran := runApp("agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db")
	ctx := context.Background()
	if err := notify.EnsureChannels(ctx, d.Write); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("UPDATE ntfy_channels SET content = 'full' WHERE name = 'assistant'"); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().Unix())
	insert := func(title string, remindAt float64, status string) int64 {
		t.Helper()
		res, err := d.Write.Exec("INSERT INTO tasks (title, notes, remind_at, status, priority, created_ts, updated_ts) VALUES (?, '', ?, ?, 'normal', ?, ?)", title, remindAt, status, now, now)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	start := func() (context.CancelFunc, chan appRun) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan appRun, 1)
		go func() { done <- runAppContext(ctx, "agent", "scheduler") }()
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
	runOnce := func() reminderPost {
		t.Helper()
		cancel, done := start()
		select {
		case p := <-posts:
			time.Sleep(300 * time.Millisecond)
			cancel()
			<-done
			return p
		case <-time.After(5 * time.Second):
			cancel()
			ran := <-done
			t.Fatalf("no reminder was posted: exit %d stderr %s", ran.code, ran.stderr)
			return reminderPost{}
		}
	}
	runNone := func() {
		t.Helper()
		cancel, done := start()
		select {
		case p := <-posts:
			cancel()
			<-done
			t.Fatalf("unexpected reminder: %#v", p)
		case <-time.After(2 * time.Second):
		}
		cancel()
		<-done
	}
	insert("Pay the rent", now-1, "open")
	due()
	if p := runOnce(); !strings.Contains(p.Title, "Pay the rent") {
		t.Fatalf("reminder title = %q", p.Title)
	}
	due()
	runNone()
	insert("Done task", now-1, "done")
	insert("Future task", now+3600, "open")
	due()
	runNone()

}
