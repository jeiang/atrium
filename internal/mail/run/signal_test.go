package run

import (
	"context"
	"os"
	"syscall"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
)

// signalling is a client that delivers sig to the process in the middle of its first COPY and waits for the
// command's context to end before the COPY goes on, as a SIGTERM that lands while a live pass is writing.
type signalling struct {
	mail.Client
	sig    syscall.Signal
	ctx    context.Context
	copies int
}

func (s *signalling) Copy(ctx context.Context, uids mail.UIDSet, dest string) (*mail.CopyUID, error) {
	s.copies++
	if s.copies == 1 {
		if err := syscall.Kill(os.Getpid(), s.sig); err != nil {
			return nil, err
		}
		<-s.ctx.Done()
	}
	return s.Client.Copy(ctx, uids, dest)
}

func liveActions(w *world) []map[string]any {
	w.t.Helper()
	rows, err := w.db.Read.Query("SELECT id, kind, ok, error, dest FROM auto_actions ORDER BY id")
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []map[string]any
	for rows.Next() {
		var id int64
		var kind, dest string
		var ok, errText any
		if err := rows.Scan(&id, &kind, &ok, &errText, &dest); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, map[string]any{"id": id, "kind": kind, "ok": ok, "error": errText, "dest": dest})
	}
	return out
}

// TestSignalDuringLivePassLeavesNoInterruptedOrDuplicatedAction sends SIGTERM and SIGINT to `atrium sync` while
// its live pass is moving a message: the move completes, its ledger row is applied (never left in flight to be
// recovered as interrupted), the account is resynced, the command exits as Python does after the signal, and
// the next pass neither repeats the move nor records anything.
func TestSignalDuringLivePassLeavesNoInterruptedOrDuplicatedAction(t *testing.T) {
	for _, c := range []struct {
		sig    syscall.Signal
		code   int
		stderr string
	}{
		{syscall.SIGTERM, cli.ExitTerminated, ""},
		{syscall.SIGINT, cli.ExitInterrupt, "KeyboardInterrupt\n"},
	} {
		t.Run(c.sig.String(), func(t *testing.T) {
			w := newWorld(t, config.Live, credentials)
			w.rule("x.test", "Bills")
			if code, _, stderr := runCLI(t, context.Background(), w, credentials, "sync", "--account", "icloud"); code != cli.ExitOK {
				t.Fatalf("baseline sync = %d, %q", code, stderr)
			}
			w.arrive(config.ICloud, "Invoice", "a@x.test")
			ctx, stop := cli.SignalContext(context.Background())
			defer stop()
			client := &signalling{sig: c.sig, ctx: ctx}
			w.Wrap = func(account string, c mail.Client) mail.Client {
				if account != config.ICloud {
					return c
				}
				client.Client = c
				return client
			}
			code, stdout, stderr := runCLI(t, ctx, w, credentials, "sync", "--account", "icloud")
			if code != c.code || stdout != "" || stderr != c.stderr {
				t.Errorf("sync after %v = %d, %q, %q; want %d, \"\", %q", c.sig, code, stdout, stderr, c.code, c.stderr)
			}
			if client.copies != 1 {
				t.Fatalf("copies = %d", client.copies)
			}
			actions := liveActions(w)
			if len(actions) != 1 || actions[0]["ok"] != int64(1) || actions[0]["error"] != nil || actions[0]["dest"] != "Bills" {
				t.Fatalf("auto_actions after the signal = %v", actions)
			}
			bills, inbox := w.fakes[config.ICloud].Boxes["Bills"], w.fakes[config.ICloud].Boxes[config.Inbox]
			if len(bills.Messages) != 1 || len(inbox.Messages) != 0 {
				t.Fatalf("Bills holds %d messages and the Inbox %d", len(bills.Messages), len(inbox.Messages))
			}
			if n := w.count("SELECT COUNT(*) FROM messages WHERE folder = 'Bills' AND gone_at IS NULL"); n != 1 {
				t.Errorf("resynced Bills rows = %d", n)
			}
			w.Wrap = nil
			if code, _, stderr := runCLI(t, context.Background(), w, credentials, "sync", "--account", "icloud"); code != cli.ExitOK {
				t.Fatalf("next sync = %d, %q", code, stderr)
			}
			if again := liveActions(w); len(again) != 1 || again[0]["ok"] != int64(1) || again[0]["error"] != nil {
				t.Fatalf("auto_actions after the next pass = %v", again)
			}
			if n := w.count("SELECT COUNT(*) FROM auto_actions WHERE error LIKE 'interrupted%'"); n != 0 {
				t.Errorf("%d interrupted rows", n)
			}
		})
	}
}
