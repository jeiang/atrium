package service_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
)

func TestBackgroundRunPanicIsRecovered(t *testing.T) {
	w := newWorld(t)
	w.runner.Registry = nil
	thread := w.thread(threads.Chat)
	run, err := w.svc.StartRun(t.Context(), thread, "go", service.Start{})
	if err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	if found := w.run(run); found.State != journal.Running {
		t.Errorf("a crashed background run is left to its lease, got %s", found.State)
	}
}

func TestCancelledRequestStillStartsAndRunsItsRun(t *testing.T) {
	w := newWorld(t)
	w.push(reply(text("a")), reply(text("b")), reply(text("c")))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	thread := w.thread(threads.Chat)
	sent, err := w.svc.Send(ctx, thread, "hello", nil)
	if err != nil || sent.Queued {
		t.Fatalf("send %+v %v", sent, err)
	}
	w.svc.Wait()
	edited, err := w.svc.EditLast(ctx, thread, "hello again", nil)
	if err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	again, err := w.svc.Regenerate(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	for _, run := range []int64{sent.ID, edited, again} {
		if found := w.run(run); found.State != journal.Done {
			t.Errorf("run %d: %s", run, found.State)
		}
	}
}

func TestCloseRefusesNewBackgroundRuns(t *testing.T) {
	w := newWorld(t)
	w.svc.Close()
	thread := w.thread(threads.Chat)
	if _, err := w.svc.StartRun(t.Context(), thread, "go", service.Start{}); err != service.ErrClosed {
		t.Errorf("start after close: %v", err)
	}
}

func TestDeleteThreadDeletesRemoteFilesWithAClientFromEnv(t *testing.T) {
	w := newWorld(t)
	var mu sync.Mutex
	var deleted []string
	next := w.srv.Config.Handler
	w.srv.Config.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			mu.Lock()
			deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/v1/files/"))
			mu.Unlock()
		}
		next.ServeHTTP(rw, r)
	})
	w.svc.Client = nil
	w.svc.Env = config.Map(map[string]string{"ANTHROPIC_API_KEY": anthropictest.APIKey, anthropic.BaseURLEnv: w.srv.URL})
	thread := w.thread(threads.Chat)
	if _, err := w.d.Write.Exec("INSERT INTO thread_files (thread_id, name, media_type, bytes, path, origin, created_at, anthropic_file_id) "+
		"VALUES (?, 'a.txt', 'text/plain', 1, '/nonexistent/a.txt', 'upload', 1, 'file_legacy')", thread); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.DeleteThread(t.Context(), thread); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(deleted) != 1 || deleted[0] != "file_legacy" {
		t.Errorf("remote deletes %v", deleted)
	}
}
