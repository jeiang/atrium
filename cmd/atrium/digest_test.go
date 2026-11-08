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
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/settings"
)

type ntfyPost struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}

func runAppContext(t *testing.T, f *fixture, ctx context.Context, args ...string) appRun {
	t.Helper()
	var out bytes.Buffer
	var errs lockedBuffer
	code := f.app.Run(ctx, args, &out, &errs)
	return appRun{code, out.String(), errs.String()}
}

func TestDailyNudgesDigestPostsWithoutAModel(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	posts := make(chan ntfyPost, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p ntfyPost
		if err := json.NewDecoder(r.Body).Decode(&p); err == nil {
			posts <- p
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db")
	now := float64(time.Now().Unix())
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := d.Write.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	setEnv(t, settingEnv(notify.KeyURL), srv.URL)
	exec("INSERT INTO proposals (id, kind, status, created_ts, title, fingerprint) VALUES (1, 'reminder', 'pending', 100, 'Call the dentist', 'fp1')")
	today := time.Unix(int64(now), 0).UTC()
	exec("INSERT INTO commitments (id, message_id, text, owner, due, status, created_ts) VALUES (1, 1, 'Send invoice', 'me', ?, 'open', 100)", today.AddDate(0, 0, -1).Format("2006-01-02"))
	start := func() (context.CancelFunc, chan appRun) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan appRun, 1)
		go func() { done <- runAppContext(t, f, ctx, "agent", "scheduler") }()
		return cancel, done
	}
	cancel, done := start()
	for i := 0; i < 50; i++ {
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM jobs WHERE name = 'daily-nudges'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	<-done
	if err := notify.EnsureChannels(settings.With(context.Background(), f.settings), d.Write); err != nil {
		t.Fatal(err)
	}
	exec("UPDATE ntfy_channels SET content = 'full' WHERE name = 'assistant'")
	exec("UPDATE jobs SET next_due = NULL WHERE name != 'daily-nudges'")
	exec("UPDATE jobs SET state = 'active', next_due = ?, lease_owner = NULL, lease_until = NULL WHERE name = 'daily-nudges'", now-1)
	requests := len(api.Requests())
	cancel, done = start()
	var first ntfyPost
	select {
	case first = <-posts:
	case <-time.After(15 * time.Second):
		cancel()
		ran := <-done
		t.Fatalf("the digest never reached the fake ntfy server: exit %d stderr %s", ran.code, ran.stderr)
	}
	cancel()
	<-done
	if len(api.Requests()) != requests {
		t.Fatalf("the digest made a model call")
	}
	if !strings.Contains(first.Message, "Call the dentist") || !strings.Contains(first.Message, "Send invoice") {
		t.Fatalf("digest does not list the items: %q", first.Message)
	}
	exec("UPDATE ntfy_channels SET content = 'title' WHERE name = 'assistant'")
	exec("UPDATE jobs SET state = 'active', next_due = ?, lease_owner = NULL, lease_until = NULL WHERE name = 'daily-nudges'", now)
	cancel, done = start()
	var second ntfyPost
	select {
	case second = <-posts:
	case <-time.After(15 * time.Second):
		cancel()
		<-done
		t.Fatal("the private digest never reached the fake ntfy server")
	}
	cancel()
	<-done
	if strings.Contains(second.Message, "Call the dentist") || strings.Contains(second.Message, "Send invoice") {
		t.Fatalf("private digest leaked titles: %q", second.Message)
	}
}
