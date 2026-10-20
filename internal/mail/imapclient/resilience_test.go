package imapclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

const appliedVerifyBoth = `C/ ^{t} STATUS INBOX \((UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)( (UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)){3}\)$
S: * STATUS INBOX (MESSAGES 2 UIDNEXT 303 UIDVALIDITY 7 HIGHESTMODSEQ 5)
S: {t} OK STATUS completed
C: {t} EXAMINE INBOX
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} UID SEARCH UID 301:302
S: * SEARCH 301 302
S: {t} OK SEARCH completed
C/ ^{t} UID FETCH 301:302 \((UID|FLAGS|BODY\.PEEK\[HEADER\])( (UID|FLAGS|BODY\.PEEK\[HEADER\])){2}\)$
S: * 1 FETCH (UID 301 FLAGS (\Seen) BODY[HEADER] {25}
S: Message-ID: <m1@x.test>
S: )
S: * 2 FETCH (UID 302 FLAGS (\Seen) BODY[HEADER] {25}
S: Message-ID: <m2@x.test>
S: )
S: {t} OK FETCH completed
`

const appliedVerifySecond = `C/ ^{t} STATUS INBOX \((UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)( (UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)){3}\)$
S: * STATUS INBOX (MESSAGES 2 UIDNEXT 303 UIDVALIDITY 7 HIGHESTMODSEQ 5)
S: {t} OK STATUS completed
C: {t} EXAMINE INBOX
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} UID SEARCH UID 302
S: * SEARCH 302
S: {t} OK SEARCH completed
C/ ^{t} UID FETCH 302 \((UID|FLAGS|BODY\.PEEK\[HEADER\])( (UID|FLAGS|BODY\.PEEK\[HEADER\])){2}\)$
S: * 2 FETCH (UID 302 FLAGS (\Seen) BODY[HEADER] {25}
S: Message-ID: <m2@x.test>
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
	script := icloudLogin + appliedVerifyBoth + appliedList + appliedList + `C: {t} SELECT INBOX
S: * OK [UIDVALIDITY 7] UIDs valid
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 301 "Bills"
! close
! accept
` + icloudLogin + appliedVerifySecond + appliedList + appliedList + `C: {t} SELECT INBOX
S: * OK [UIDVALIDITY 7] UIDs valid
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 302 "Misc"
S: {t} OK [COPYUID 98 302 600] Done
C: {t} UID STORE 302 +FLAGS.SILENT (\Deleted)
S: {t} OK STORE completed
C: {t} UID EXPUNGE 302
S: * 2 EXPUNGE
S: {t} OK EXPUNGE completed
` + logout
	client, _ := session(t, imapscript.MustParse(t, script), "atrium.test@icloud.com", "app-specific password", live)
	a, d, _ := wireApplier(t, config.ICloud, client, nil, [2]any{301, "Bills"}, [2]any{302, "Misc"})
	out, err := a.ApplyAccount(ctx, config.ICloud)
	must(t, err)
	if out.Written != 0 || out.Failed < 1 {
		t.Fatalf("first pass %+v", out)
	}
	var held int
	must(t, d.Read.QueryRow("SELECT COUNT(*) FROM auto_actions WHERE ok IS NULL AND error LIKE ? AND src = 'INBOX' AND dest = 'Bills'", mailops.UnknownPrefix+"%").Scan(&held))
	if held != 1 {
		t.Fatalf("held rows = %d, rows %q", held, actionRows(t, d))
	}
	out, err = a.ApplyAccount(ctx, config.ICloud)
	must(t, err)
	if out.Written != 1 {
		t.Fatalf("second pass %+v, rows %q", out, actionRows(t, d))
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

func TestWatchReconnectsAfterASilentDropDuringIdle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := &recorder{stopAt: -1}
	script := watchEmptyPass() + "! accept\n" +
		icloudLogin + `C: {t} EXAMINE INBOX
S: * 0 EXISTS
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} IDLE
S: + idling
C: DONE
C: {t} IDLE
! sleep 5s
! accept
` + watchEmptyPass() + "! accept\n" +
		icloudLogin + `C: {t} LOGOUT
S: * BYE Logging out
S: {t} OK LOGOUT completed
`
	var start time.Time
	done := startWireWatch(t, script, ctx, clock.sleep, func(opened int) {
		if opened == 2 {
			start = time.Now()
		}
		if opened == 4 {
			cancel()
		}
	}, func(c *conn) {
		c.idleKeepalive = 150 * time.Millisecond
		c.readTimeout = 400 * time.Millisecond
	})
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("the watch loop did not stop")
	}
	if want := []time.Duration{5 * time.Second}; fmt.Sprint(clock.waits) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v", clock.waits, want)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("the silent link was noticed after %v", time.Since(start))
	}
}

func TestTapRecordsOnlyCommandsWrittenInFull(t *testing.T) {
	server, clientSide := net.Pipe()
	tp := newTap(clientSide, 0)
	got := make(chan int, 4)
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := server.Read(buf)
			if err != nil {
				return
			}
			got <- n
		}
	}()
	if _, err := tp.Write([]byte("T1 APPEND \"Sent\" {5}\r\n")); err != nil {
		t.Fatal(err)
	}
	if tp.written("APPEND", 0) {
		t.Fatal("an APPEND whose literal was never sent counts as written")
	}
	if _, err := tp.Write([]byte("hello\r\n")); err != nil {
		t.Fatal(err)
	}
	if !tp.written("APPEND", 0) {
		t.Fatal("a completed APPEND does not count as written")
	}
	_ = server.Close()
	if n, err := tp.Write([]byte("T2 UID COPY 1 \"Bills\"\r\n")); err == nil || n != 0 {
		t.Fatalf("write to a closed link = %d, %v", n, err)
	}
	if tp.written("UID", 0) {
		t.Fatal("a command that reached no byte counts as written")
	}
}
