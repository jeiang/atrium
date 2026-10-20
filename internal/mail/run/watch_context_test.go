package run

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
)

type pausedMailboxList struct {
	mail.Client
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (c *pausedMailboxList) ListMailboxes(ctx context.Context) ([]mail.MailboxInfo, error) {
	c.once.Do(func() { close(c.started) })
	<-c.release
	return c.Client.ListMailboxes(ctx)
}

func TestWatchFinishesPassWhenCancelledDuringSyncOne(t *testing.T) {
	w := newWorld(t, config.Shadow, credentials)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	w.Wrap = func(account string, client mail.Client) mail.Client {
		if account == config.ICloud {
			return &pausedMailboxList{Client: client, started: started, release: release}
		}
		return client
	}
	processed := make(chan struct{}, 1)
	w.pass.Events = func(context.Context, string) (int, int, error) {
		processed <- struct{}{}
		return 0, 0, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.watcher(func(context.Context, time.Duration) {}).Account(ctx, config.ICloud)
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("watch pass did not start SyncOne")
	}
	cancel()
	unblock()
	select {
	case <-processed:
	case <-time.After(10 * time.Second):
		t.Fatal("watch pass did not finish after shutdown was requested")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("watch loop did not exit after its pass finished")
	}
}
