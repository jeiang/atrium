package imapclient

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/testutil/imapscript"
)

const appliedVerifyInbox = `C/ ^{t} STATUS INBOX \((UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)( (UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)){3}\)$
S: * STATUS INBOX (MESSAGES 1 UIDNEXT 302 UIDVALIDITY 7 HIGHESTMODSEQ 5)
S: {t} OK STATUS completed
C: {t} EXAMINE INBOX
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} UID SEARCH UID 301
S: * SEARCH 301
S: {t} OK SEARCH completed
C/ ^{t} UID FETCH 301 \((UID|FLAGS|BODY\.PEEK\[HEADER\])( (UID|FLAGS|BODY\.PEEK\[HEADER\])){2}\)$
S: * 1 FETCH (UID 301 FLAGS (\Seen) BODY[HEADER] {25}
S: Message-ID: <m1@x.test>
S: )
S: {t} OK FETCH completed
`

func asWriteInterrupted(t *testing.T, err error) *mail.WriteInterrupted {
	t.Helper()
	var wi *mail.WriteInterrupted
	if !errors.As(err, &wi) {
		t.Fatalf("err = %#v, want a *mail.WriteInterrupted", err)
	}
	return wi
}

func TestDropDuringCopyHoldsAndNeverRecopies(t *testing.T) {
	ctx := context.Background()
	script := icloudLogin + appliedVerifyInbox + appliedList + appliedList + `C: {t} SELECT INBOX
S: * OK [UIDVALIDITY 7] UIDs valid
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 301 "Bills"
! close
`
	client, _ := session(t, imapscript.MustParse(t, script), "atrium.test@icloud.com", "app-specific password", live)
	a, d, _ := wireApplier(t, config.ICloud, client, nil, [2]any{301, "Bills"})
	out, err := a.ApplyAccount(ctx, config.ICloud)
	must(t, err)
	if out.Written != 0 || out.Failed != 1 {
		t.Fatalf("first pass %+v", out)
	}
	rows, err := d.Read.Query("SELECT ok, error FROM auto_actions")
	must(t, err)
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		t.Fatal("no auto_actions row")
	}
	var ok *int64
	var text string
	must(t, rows.Scan(&ok, &text))
	if ok != nil || !strings.HasPrefix(text, mailops.UnknownPrefix) {
		t.Fatalf("row ok=%v error=%q, want held with an unknown outcome", ok, text)
	}
	out, err = a.ApplyAccount(ctx, config.ICloud)
	must(t, err)
	if out.Written != 0 {
		t.Fatalf("second pass %+v", out)
	}
}

func TestDropDuringStoreKeepsThePartialMove(t *testing.T) {
	ctx := context.Background()
	client, _ := icloud(t, icloudLogin+`C: {t} SELECT INBOX
S: * OK [UIDVALIDITY 7] UIDs valid
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 301 "Bills"
S: {t} OK [COPYUID 99 301 500] Done
C: {t} UID STORE 301 +FLAGS.SILENT (\Deleted)
! close
`, live)
	must(t, client.Connect(ctx))
	err := mailops.MoveUIDs(ctx, client, "INBOX", "Bills", 7, []uint32{301}, func([]uint32, *mail.CopyUID) error {
		t.Fatal("an interrupted batch was reported moved")
		return nil
	})
	partial, ok := errors.AsType[*mailops.PartialMove](err)
	if !ok || partial.Unconfirmed || partial.Copied == nil || partial.Copied.Mapping[301] != 500 {
		t.Fatalf("err = %#v", err)
	}
	_ = asWriteInterrupted(t, err)
}

func TestDropDuringExpungeKeepsThePartialMove(t *testing.T) {
	ctx := context.Background()
	client, _ := icloud(t, icloudLogin+`C: {t} SELECT INBOX
S: * OK [UIDVALIDITY 7] UIDs valid
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 301 "Bills"
S: {t} OK [COPYUID 99 301 500] Done
C: {t} UID STORE 301 +FLAGS.SILENT (\Deleted)
S: {t} OK STORE completed
C: {t} UID EXPUNGE 301
! close
`, live)
	must(t, client.Connect(ctx))
	err := mailops.MoveUIDs(ctx, client, "INBOX", "Bills", 7, []uint32{301}, nil)
	partial, ok := errors.AsType[*mailops.PartialMove](err)
	if !ok || partial.Unconfirmed || partial.Copied == nil || partial.Copied.Mapping[301] != 500 {
		t.Fatalf("err = %#v", err)
	}
	_ = asWriteInterrupted(t, err)
}

func TestDropDuringCopyIsAnUnconfirmedPartialMove(t *testing.T) {
	ctx := context.Background()
	client, _ := icloud(t, icloudLogin+`C: {t} SELECT INBOX
S: * OK [UIDVALIDITY 7] UIDs valid
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 301 "Bills"
! close
`, live)
	must(t, client.Connect(ctx))
	err := mailops.MoveUIDs(ctx, client, "INBOX", "Bills", 7, []uint32{301}, nil)
	partial, ok := errors.AsType[*mailops.PartialMove](err)
	if !ok || !partial.Unconfirmed || partial.Copied != nil || !mailops.Blocks(err.Error()) {
		t.Fatalf("err = %#v", err)
	}
	_ = asWriteInterrupted(t, err)
}

func TestDropDuringAppendIsNotRetried(t *testing.T) {
	ctx := context.Background()
	client, _ := icloud(t, icloudLogin+`C/ ^{t} APPEND "Sent Messages" \(\\Seen\) "[^"]+" \{\d+\+?\}$
! close
`, live)
	must(t, client.Connect(ctx))
	err := client.Append(ctx, "Sent Messages", []byte("Subject: x\r\n\r\nbody\r\n"), `\Seen`)
	wi := asWriteInterrupted(t, err)
	if wi.Command != "APPEND" || py.ExceptionText(err) == "" {
		t.Fatalf("err = %#v", err)
	}
}

func TestServerRejectionIsNeverWriteInterrupted(t *testing.T) {
	ctx := context.Background()
	client, _ := icloud(t, icloudLogin+`C: {t} SELECT INBOX
S: * OK [UIDVALIDITY 7] UIDs valid
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 301 "Bills"
S: {t} NO [TRYCREATE] no such mailbox
`+logout, live)
	must(t, client.Connect(ctx))
	selectRW(t, ctx, client, "INBOX")
	_, err := client.Copy(ctx, mail.UIDs(301), "Bills")
	var wi *mail.WriteInterrupted
	if err == nil || errors.As(err, &wi) {
		t.Fatalf("Copy = %v", err)
	}
	client.Close()
}

func TestStalledWriteTimesOut(t *testing.T) {
	server, clientSide := net.Pipe()
	defer func() { _ = server.Close() }()
	tp := newTap(clientSide, 100*time.Millisecond)
	start := time.Now()
	_, err := tp.Write(bytes.Repeat([]byte("x"), 1<<20))
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("Write = %v after %v", err, time.Since(start))
	}
	if failure := tp.failure(); !errors.Is(failure, errTimedOut) {
		t.Fatalf("failure = %v", failure)
	}
}

func TestIdleDetectsASilentDropWithinTheKeepaliveWindow(t *testing.T) {
	ctx := context.Background()
	client, c := icloud(t, icloudLogin+`C: {t} EXAMINE INBOX
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} IDLE
S: + idling
C: DONE
C: {t} IDLE
! sleep 3s
`, live)
	c.idleKeepalive = 100 * time.Millisecond
	c.readTimeout = 150 * time.Millisecond
	must(t, client.Connect(ctx))
	must(t, client.Select(ctx, "INBOX"))
	start := time.Now()
	changed, err := client.IdleChanges(ctx, time.Minute)
	if changed || err == nil || !strings.Contains(err.Error(), "timed out") || time.Since(start) > 2*time.Second {
		t.Fatalf("IdleChanges = %v, %v after %v", changed, err, time.Since(start))
	}
}
