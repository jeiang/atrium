package imapclient

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailcmd"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/executors"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/testutil/imapscript"
)

// terminating delivers SIGTERM to the process shortly after its first STORE has gone out, while the scripted
// server holds the reply, as a shutdown that lands in the middle of a move.
type terminating struct {
	mail.Client
	sent bool
}

func (c *terminating) StoreFlags(ctx context.Context, uids mail.UIDSet, add bool, flags ...string) error {
	if !c.sent {
		c.sent = true
		time.AfterFunc(200*time.Millisecond, func() { _ = syscall.Kill(os.Getpid(), syscall.SIGTERM) })
	}
	return c.Client.StoreFlags(ctx, uids, add, flags...)
}

type shutdownWorld struct {
	t     *testing.T
	db    *db.DB
	opens int
	srv   *imapscript.Server
}

func newShutdownWorld(t *testing.T, script string) *shutdownWorld {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	t.Setenv("ATRIUM_MODE", "live")
	t.Setenv("ATRIUM_DB", path)
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ICLOUD_MAIL_USERNAME", "atrium.test@icloud.com")
	t.Setenv("ICLOUD_APP_PASSWORD", "app-specific password")
	d, err := db.Connect(path)
	must(t, err)
	t.Cleanup(func() { _ = d.Close() })
	must(t, db.Migrate(context.Background(), d, path))
	w := &shutdownWorld{t: t, db: d, srv: imapscript.Serve(t, imapscript.MustParse(t, script))}
	w.exec("INSERT INTO accounts (name, kind) VALUES ('icloud', 'icloud')")
	w.exec("INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (1, 'icloud', 'INBOX', 'INBOX', 'inbox')")
	w.exec("INSERT INTO messages (id, account, mailbox_id, uidvalidity, uid, msgid, digest, first_seen) VALUES (1, 'icloud', 1, 7, 301, 'a@x.test', 'da', 0)")
	w.exec("INSERT INTO messages (id, account, mailbox_id, uidvalidity, uid, msgid, digest, first_seen) VALUES (2, 'icloud', 1, 7, 302, 'b@x.test', 'db', 0)")
	return w
}

func (w *shutdownWorld) exec(query string, args ...any) {
	w.t.Helper()
	if _, err := w.db.Write.Exec(query, args...); err != nil {
		w.t.Fatal(err)
	}
}

func (w *shutdownWorld) text(query string, args ...any) string {
	w.t.Helper()
	var out *string
	if err := w.db.Read.QueryRow(query, args...).Scan(&out); err != nil {
		w.t.Fatal(err)
	}
	if out == nil {
		return "<null>"
	}
	return *out
}

func (w *shutdownWorld) clients(config.Config, config.Env) func(context.Context, string) (mail.Client, error) {
	return func(ctx context.Context, _ string) (mail.Client, error) {
		w.opens++
		if err := ctx.Err(); err != nil || w.opens > 1 {
			return nil, context.Cause(ctx)
		}
		c := newConn("imap.test", "atrium.test@icloud.com", "app-specific password")
		c.dial = func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", w.srv.Addr())
		}
		c.readTimeout = 5 * time.Second
		client := mail.Gated(c, config.Map(map[string]string{"ATRIUM_MODE": "live"}))
		if err := client.Connect(ctx); err != nil {
			return nil, err
		}
		return &terminating{Client: client}, nil
	}
}

func (w *shutdownWorld) run(args ...string) (int, string, string) {
	w.t.Helper()
	ctx, stop := cli.SignalContext(context.Background())
	defer stop()
	opts := mailcmd.Options{Clients: w.clients, Now: func() float64 { return 2 }}
	app := cli.New("atrium", nil)
	app.Add(opts.ProposalsCommand(), opts.LiveCommand())
	var stdout, stderr bytes.Buffer
	code := app.Run(ctx, args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

const shutdownMove = icloudLogin + verifyInbox + `C: {t} CREATE "Receipts/Shop"
S: {t} OK CREATE completed
C: {t} SUBSCRIBE "Receipts/Shop"
S: {t} OK SUBSCRIBE completed
C: {t} SELECT INBOX
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 301:302 "Receipts/Shop"
S: {t} OK [COPYUID 9 301:302 1:2] Done
C: {t} UID STORE 301:302 +FLAGS.SILENT (\Deleted)
! sleep 1s
`

const shutdownMoveBack = icloudLogin + `C/ ^{t} STATUS "Bills" \((UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)( (UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)){3}\)$
S: * STATUS "Bills" (MESSAGES 1 UIDNEXT 501 UIDVALIDITY 99 HIGHESTMODSEQ 9)
S: {t} OK STATUS completed
C: {t} EXAMINE "Bills"
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} UID SEARCH UID 500
S: * SEARCH 500
S: {t} OK SEARCH completed
C: {t} SELECT "Bills"
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 500 INBOX
S: {t} OK [COPYUID 7 500 40] Done
C: {t} UID STORE 500 +FLAGS.SILENT (\Deleted)
! sleep 1s
`

const moveBackPlan = `{"op": "move_back", "account": "icloud", "src_imap": "INBOX", "dest_imap": "Bills", "dest_uidvalidity": 99, ` +
	`"items": [{"id": 1, "msgid": "a@x.test", "uid": 301, "dest_uid": 500}]}`

func (w *shutdownWorld) proposal(kind string) int64 {
	w.t.Helper()
	ctx := context.Background()
	snaps, err := mailops.Snapshots(ctx, w.db.Read, []int64{1, 2})
	must(w.t, err)
	list := make([]any, len(snaps))
	for i, s := range snaps {
		list[i] = s.Dict()
	}
	account := config.ICloud
	id, _, err := proposals.Create(ctx, w.db, kind, &account, "title", backlogPayload("Receipts/Shop")(list), py.Dict{}, "fp", 1)
	must(w.t, err)
	return id
}

func TestProposalsApproveSigtermKillsTheConnectionAndMarksTheProposalFailed(t *testing.T) {
	w := newShutdownWorld(t, shutdownMove)
	id := w.proposal(executors.InboxBacklog)
	code, _, stderr := w.run("proposals", "approve", strconv.FormatInt(id, 10))
	if code != cli.ExitTerminated || stderr != "" {
		t.Fatalf("approve after SIGTERM = %d, %q", code, stderr)
	}
	if status := w.text("SELECT status FROM proposals WHERE id = ?", id); status != proposals.Failed {
		t.Fatalf("status = %s", status)
	}
	want := `{"op": "move", "account": "icloud", "src_imap": "INBOX", "src_uidvalidity": 7, "dest_imap": "Receipts/Shop", "dest_uidvalidity": 9, "copied": {"301": 1, "302": 2}}`
	if got := w.text("SELECT action FROM proposal_actions WHERE proposal_id = ? AND ok = 0", id); got != want {
		t.Fatalf("partial move logged as %s", got)
	}
}

func TestProposalsUndoSigtermKillsTheConnectionAndLogsTheFailedUndo(t *testing.T) {
	w := newShutdownWorld(t, shutdownMoveBack)
	id := w.proposal(executors.InboxBacklog)
	w.exec("UPDATE proposals SET status = 'applied', result = '{}' WHERE id = ?", id)
	w.exec("INSERT INTO proposal_actions (proposal_id, ts, action, undo, ok) VALUES (?, 1, '{\"op\": \"move\"}', ?, 1)", id, moveBackPlan)
	code, _, stderr := w.run("proposals", "undo", strconv.FormatInt(id, 10))
	if code != cli.ExitTerminated || stderr != "" {
		t.Fatalf("undo after SIGTERM = %d, %q", code, stderr)
	}
	if got := w.text("SELECT error FROM proposal_actions WHERE proposal_id = ? AND ok = 0", id); !strings.Contains(got, "copied but not expunged") {
		t.Fatalf("failed undo logged as %q", got)
	}
	if got := w.text("SELECT result FROM proposals WHERE id = ?", id); got != "{}" {
		t.Fatalf("result = %s", got)
	}
}

func TestLiveUndoSigtermKillsTheConnectionAndRecordsTheFailure(t *testing.T) {
	w := newShutdownWorld(t, shutdownMoveBack)
	w.exec("INSERT INTO auto_actions (id, ts, account, message_id, kind, src, dest, undo, ok) VALUES (1, 1, 'icloud', 1, 'file', 'INBOX', 'Bills', ?, 1)", moveBackPlan)
	code, _, stderr := w.run("live", "undo", "1")
	if code != cli.ExitTerminated || stderr != "" {
		t.Fatalf("live undo after SIGTERM = %d, %q", code, stderr)
	}
	if got := w.text("SELECT undone_error FROM auto_actions WHERE id = 1"); !strings.Contains(got, "copied but not expunged") {
		t.Fatalf("undone_error = %q", got)
	}
	if got := w.text("SELECT undone_ts FROM auto_actions WHERE id = 1"); got != "<null>" {
		t.Fatalf("undone_ts = %s", got)
	}
}
