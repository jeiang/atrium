package run

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
)

func shortDeadline(t *testing.T, d time.Duration) {
	t.Helper()
	old := passDeadline
	passDeadline = d
	t.Cleanup(func() { passDeadline = old })
}

type slowCopy struct {
	mail.Client
	delay time.Duration
	once  bool
}

func (s *slowCopy) Copy(ctx context.Context, uids mail.UIDSet, dest string) (*mail.CopyUID, error) {
	if !s.once {
		s.once = true
		time.Sleep(s.delay)
	}
	return s.Client.Copy(ctx, uids, dest)
}

type stuckList struct{ mail.Client }

func (s stuckList) ListMailboxes(ctx context.Context) ([]mail.MailboxInfo, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestPassDeadlineLetsAStartedWriteFinish(t *testing.T) {
	w := newWorld(t, config.Live, credentials)
	w.rule("x.test", "Bills")
	if code, _, stderr := runCLI(t, context.Background(), w, credentials, "sync", "--account", "icloud"); code != cli.ExitOK {
		t.Fatalf("baseline sync = %d, %q", code, stderr)
	}
	w.arrive(config.ICloud, "Invoice", "a@x.test")
	shortDeadline(t, 300*time.Millisecond)
	w.Wrap = func(account string, c mail.Client) mail.Client {
		if account != config.ICloud {
			return c
		}
		return &slowCopy{Client: c, delay: 700 * time.Millisecond}
	}
	code, stdout, stderr := runCLI(t, context.Background(), w, credentials, "sync", "--account", "icloud")
	if code != cli.ExitError || stdout != "" || !strings.Contains(stderr, "deadline exceeded") {
		t.Fatalf("sync past its deadline = %d, %q, %q", code, stdout, stderr)
	}
	actions := liveActions(w)
	if len(actions) != 1 || actions[0]["ok"] != int64(1) || actions[0]["error"] != nil || actions[0]["dest"] != "Bills" {
		t.Fatalf("auto_actions = %v", actions)
	}
	bills, inbox := w.fakes[config.ICloud].Boxes["Bills"], w.fakes[config.ICloud].Boxes[config.Inbox]
	if len(bills.Messages) != 1 || len(inbox.Messages) != 0 {
		t.Fatalf("Bills holds %d messages and the Inbox %d", len(bills.Messages), len(inbox.Messages))
	}
	if n := w.count("SELECT COUNT(*) FROM messages WHERE folder = 'Bills' AND gone_at IS NULL"); n != 1 {
		t.Errorf("resynced Bills rows = %d", n)
	}
}

func TestPassDeadlineEndsAStuckStageAndReleasesTheLock(t *testing.T) {
	w := newWorld(t, config.Live, credentials)
	w.rule("x.test", "Bills")
	shortDeadline(t, 200*time.Millisecond)
	w.Wrap = func(account string, c mail.Client) mail.Client { return stuckList{c} }
	started := time.Now()
	code, _, stderr := runCLI(t, context.Background(), w, credentials, "sync", "--account", "icloud")
	if code != cli.ExitError || !strings.Contains(stderr, "deadline exceeded") {
		t.Fatalf("stuck sync = %d, %q", code, stderr)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("sync took %v", elapsed)
	}
	if n := w.count("SELECT COUNT(*) FROM auto_actions"); n != 0 {
		t.Errorf("auto_actions = %d", n)
	}
	w.Wrap = nil
	shortDeadline(t, time.Minute)
	if code, _, stderr := runCLI(t, context.Background(), w, credentials, "sync", "--account", "icloud"); code != cli.ExitOK {
		t.Fatalf("next sync = %d, %q", code, stderr)
	}
}
