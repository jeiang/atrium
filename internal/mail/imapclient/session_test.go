package imapclient

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/safety"
	"github.com/jeiang/atrium/internal/testutil/imapscript"
)

var (
	live   = config.Map(map[string]string{"ATRIUM_MODE": "live"})
	shadow = config.Map(map[string]string{"ATRIUM_MODE": "shadow"})
)

func session(t *testing.T, s *imapscript.Script, user, password string, env config.Env) (mail.Client, *conn) {
	t.Helper()
	srv := imapscript.Serve(t, s)
	c := newConn("imap.test", user, password)
	c.dial = func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", srv.Addr())
	}
	c.readTimeout = 5 * time.Second
	client := mail.Gated(c, env)
	t.Cleanup(client.Close)
	return client, c
}

func icloud(t *testing.T, script string, env config.Env) (mail.Client, *conn) {
	t.Helper()
	return session(t, imapscript.MustParse(t, script), "atrium.test@icloud.com", "app-specific password", env)
}

const icloudLogin = `S: {icloud.greeting}
C: {t} LOGIN "atrium.test@icloud.com" "app-specific password"
S: {t} OK [CAPABILITY {icloud.authcaps}] User atrium.test logged in
`

const logout = `C: {t} LOGOUT
S: * BYE LOGOUT received
S: {t} OK LOGOUT completed
`

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantError(t *testing.T, err error, kind, msg string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Kind != kind || e.Msg != msg {
		t.Fatalf("err = %#v, want imaplib.%s(%q)", err, kind, msg)
	}
}

func TestICloudSession(t *testing.T) {
	ctx := context.Background()
	client, _ := session(t, imapscript.Load(t, "testdata/icloud.txt"), "atrium.test@icloud.com", "app-specific password", live)
	must(t, client.Connect(ctx))
	for _, cap := range []string{"IDLE", "CONDSTORE", "UIDPLUS", "QRESYNC", "LITERAL+"} {
		if !client.HasCapability(cap) {
			t.Errorf("capability %s missing after LOGIN", cap)
		}
	}
	if client.HasCapability("MOVE") || client.HasCapability("X-GM-EXT-1") {
		t.Error("iCloud advertises neither MOVE nor X-GM-EXT-1")
	}

	boxes, err := client.ListMailboxes(ctx)
	must(t, err)
	want := []mail.MailboxInfo{
		{Name: "INBOX", IMAPName: "INBOX", Flags: []string{`\HasNoChildren`}, Delimiter: "/"},
		{Name: "Sent Messages", IMAPName: "Sent Messages", Flags: []string{`\HasNoChildren`, `\Sent`}, Delimiter: "/"},
		{Name: "Deleted Messages", IMAPName: "Deleted Messages", Flags: []string{`\HasNoChildren`, `\Trash`}, Delimiter: "/"},
		{Name: "Café", IMAPName: "Caf&AOk-", Flags: []string{`\HasChildren`}, Delimiter: "/"},
		{Name: `Café/Re"ceipts`, IMAPName: `Caf&AOk-/Re"ceipts`, Flags: []string{`\HasNoChildren`}, Delimiter: "/"},
	}
	if !reflect.DeepEqual(boxes, want) {
		t.Fatalf("ListMailboxes = %#v", boxes)
	}

	st, err := client.Status(ctx, "INBOX")
	must(t, err)
	if st.UIDValidity != 1544650101 || st.UIDNext != 304 || st.Messages != 3 || st.HighestModSeq == nil || *st.HighestModSeq != 81250 {
		t.Fatalf("Status = %+v", st)
	}

	must(t, client.Select(ctx, "INBOX"))
	uids, err := client.UIDSearch(ctx, mail.SearchCriteria{})
	must(t, err)
	if !reflect.DeepEqual(uids, []uint32{301, 302, 303}) {
		t.Fatalf("UIDSearch = %v", uids)
	}

	msgs, err := client.UIDFetch(ctx, mail.UIDs(303), mail.FetchItems{Header: true, Body: true})
	must(t, err)
	wantMsg := mail.Message{
		UID: 303, Flags: []string{`\Seen`}, Size: 1234, InternalDate: " 5-Oct-2026 10:11:12 -0700",
		Attachments: []mail.Attachment{{Kind: "application/pdf", Name: "=?UTF-8?B?w6kucGRm?="}},
		Header:      []byte("Subject: Hi\r\n"), Text: []byte("body"),
	}
	if len(msgs) != 1 || !reflect.DeepEqual(msgs[0], wantMsg) {
		t.Fatalf("UIDFetch = %#v", msgs)
	}

	msgs, err = client.UIDFetch(ctx, mail.AllUIDs(), mail.FetchItems{ChangedSince: 81200})
	must(t, err)
	type change struct {
		uid    uint32
		modseq uint64
		flags  []string
	}
	var changes []change
	for _, m := range msgs {
		changes = append(changes, change{m.UID, m.ModSeq, m.Flags})
	}
	if want := []change{{302, 81230, []string{`\Seen`}}, {303, 81250, []string{`\Seen`, `\Flagged`}}}; !reflect.DeepEqual(changes, want) {
		t.Fatalf("CHANGEDSINCE = %+v", changes)
	}

	must(t, client.SelectRW(ctx, "INBOX"))
	copied, err := client.Copy(ctx, mail.UIDs(301, 302), "Deleted Messages")
	must(t, err)
	if want := (&mail.CopyUID{UIDValidity: 1544650202, Mapping: map[uint32]uint32{301: 900, 302: 901}}); !reflect.DeepEqual(copied, want) {
		t.Fatalf("Copy = %+v", copied)
	}
	must(t, client.StoreFlags(ctx, mail.UIDs(301, 302), true, `\Deleted`))
	must(t, client.UIDExpunge(ctx, mail.UIDs(301, 302)))

	must(t, client.Append(ctx, "Sent Messages", []byte("Subject: x\nline\rend")))

	must(t, client.Select(ctx, "INBOX"))
	changed, err := client.IdleChanges(ctx, time.Minute)
	must(t, err)
	if !changed {
		t.Fatal("IdleChanges missed the EXISTS")
	}
	client.Close()
}

func TestGmailSession(t *testing.T) {
	ctx := context.Background()
	client, _ := session(t, imapscript.Load(t, "testdata/gmail.txt"), "atrium.test@gmail.com", "app password", live)
	must(t, client.Connect(ctx))
	if !client.HasCapability("X-GM-EXT-1") || !client.HasCapability("CONDSTORE") || !client.HasCapability("UIDPLUS") {
		t.Fatal("Gmail capabilities after LOGIN")
	}

	st, err := client.Status(ctx, "[Gmail]/All Mail")
	must(t, err)
	if st.Messages != 4 || st.HighestModSeq == nil || *st.HighestModSeq != 9123456 {
		t.Fatalf("Status = %+v", st)
	}
	must(t, client.Select(ctx, "[Gmail]/All Mail"))

	msgs, err := client.UIDFetch(ctx, mail.UIDs(101), mail.FetchItems{Labels: true, Header: true, Body: true})
	must(t, err)
	want := mail.Message{
		UID: 101, Flags: []string{`\Seen`}, Labels: []string{`\Inbox`, `\Important`, "Café", "Muy Importante"},
		Size: 4286, GmMsgID: "18446744073709551615", GmThrID: "1278455344230334865", InternalDate: "17-Jul-2026 02:44:25 +0000",
		Attachments: []mail.Attachment{}, Header: []byte("From: a@b.cc\r\n"), Text: []byte("Hi there"),
	}
	if len(msgs) != 1 || !reflect.DeepEqual(msgs[0], want) {
		t.Fatalf("UIDFetch = %#v", msgs)
	}

	msgs, err = client.UIDFetch(ctx, mail.UIDs(101, 102), mail.FetchItems{Labels: true})
	must(t, err)
	if len(msgs) != 2 || !reflect.DeepEqual(msgs[0].Labels, []string{`\Inbox`}) || !reflect.DeepEqual(msgs[1].Labels, []string{}) || !reflect.DeepEqual(msgs[1].Flags, []string{}) {
		t.Fatalf("label fetch = %#v", msgs)
	}

	promotions, err := client.UIDSearch(ctx, mail.SearchCriteria{GmailRaw: "category:promotions"})
	must(t, err)
	social, err := client.UIDSearch(ctx, mail.SearchCriteria{GmailRaw: "category:social"})
	must(t, err)
	if !reflect.DeepEqual(promotions, []uint32{102}) || len(social) != 0 {
		t.Fatalf("X-GM-RAW: %v %v", promotions, social)
	}

	must(t, client.SelectRW(ctx, "[Gmail]/All Mail"))
	must(t, client.StoreLabels(ctx, mail.UIDs(101, 102), true, `Café/Re"ceipts`, `\Important`))
	must(t, client.StoreLabels(ctx, mail.UIDs(101), false, `\Inbox`))
	copied, err := client.Copy(ctx, mail.UIDs(101), "[Gmail]/Trash")
	if err != nil || copied != nil {
		t.Fatalf("Copy without COPYUID = %+v, %v", copied, err)
	}

	name := mail.UTF7Encode("Café/2026")
	must(t, client.CreateMailbox(ctx, name))
	must(t, client.SubscribeMailbox(ctx, name))
	must(t, client.DeleteMailbox(ctx, name))
	client.Close()
}

func TestRefusedWritesSendNothing(t *testing.T) {
	ctx := context.Background()
	client, _ := icloud(t, icloudLogin+`C: {t} SELECT INBOX
S: {t} OK [READ-WRITE] SELECT completed
`+logout, shadow)
	must(t, client.Connect(ctx))
	set := mail.UIDs(1)
	writes := map[string]func() error{
		"SelectRW":         func() error { return client.SelectRW(ctx, "INBOX") },
		"Copy":             func() error { _, err := client.Copy(ctx, set, "Archive"); return err },
		"StoreFlags":       func() error { return client.StoreFlags(ctx, set, true, `\Deleted`) },
		"StoreLabels":      func() error { return client.StoreLabels(ctx, set, true, "x") },
		"UIDExpunge":       func() error { return client.UIDExpunge(ctx, set) },
		"Append":           func() error { return client.Append(ctx, "INBOX", []byte("x")) },
		"CreateMailbox":    func() error { return client.CreateMailbox(ctx, "New") },
		"SubscribeMailbox": func() error { return client.SubscribeMailbox(ctx, "New") },
		"DeleteMailbox":    func() error { return client.DeleteMailbox(ctx, "New") },
	}
	for name, write := range writes {
		var refused safety.WriteRefused
		if err := write(); !errors.As(err, &refused) {
			t.Errorf("%s in shadow mode: err %v, want a refusal", name, err)
		}
	}
	must(t, client.SelectRW(safety.ApprovedWrites(ctx, "1"), "INBOX"))
	client.Close()
}

func TestCapabilitiesRequestedAfterLoginWithoutList(t *testing.T) {
	client, _ := icloud(t, `S: {icloud.greeting}
C: {t} LOGIN "atrium.test@icloud.com" "app-specific password"
S: {t} OK User atrium.test logged in
C: {t} CAPABILITY
S: * CAPABILITY IMAP4rev1 IDLE uidplus
S: {t} OK CAPABILITY completed
`+logout, live)
	must(t, client.Connect(context.Background()))
	if !client.HasCapability("IDLE") || !client.HasCapability("UIDPLUS") || client.HasCapability("AUTH=PLAIN") {
		t.Fatal("capabilities must be the post-LOGIN CAPABILITY reply, upper-cased")
	}
	client.Close()
}

func TestUntaggedCapabilityDuringLoginIsUsed(t *testing.T) {
	client, _ := icloud(t, `S: * OK [CAPABILITY IMAP4rev1 AUTH=PLAIN] ready
C: {t} LOGIN "atrium.test@icloud.com" "app-specific password"
S: * CAPABILITY IMAP4rev1 IDLE
S: {t} OK [CAPABILITY IMAP4rev1 CONDSTORE] logged in
`+logout, live)
	must(t, client.Connect(context.Background()))
	if !client.HasCapability("CONDSTORE") || client.HasCapability("IDLE") {
		t.Fatal("the last CAPABILITY data (the LOGIN code) wins")
	}
	client.Close()
}

func TestConnectFailures(t *testing.T) {
	tests := []struct {
		name, script, kind, msg string
	}{
		{"login rejected", `S: {icloud.greeting}
C: {t} LOGIN "atrium.test@icloud.com" "app-specific password"
S: {t} NO [AUTHENTICATIONFAILED] Authentication failed
`, "error", "[AUTHENTICATIONFAILED] Authentication failed"},
		{"login BAD", `S: {icloud.greeting}
C: {t} LOGIN "atrium.test@icloud.com" "app-specific password"
S: {t} BAD Invalid
`, "error", `LOGIN command error: BAD [b'Invalid']`},
		{"not IMAP4", "S: * OK [CAPABILITY IMAP5 AUTH=PLAIN] hi\n", "error", "server not IMAP4 compliant"},
		{"greeting BYE", "S: * BYE [UNAVAILABLE] busy\n", "error", "invalid greeting: * BYE [UNAVAILABLE] busy"},
		{"dropped", "S: {icloud.greeting}\nC: {t} LOGIN \"atrium.test@icloud.com\" \"app-specific password\"\n! close\n",
			"abort", "command: LOGIN => socket error: EOF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, c := icloud(t, tt.script, live)
			wantError(t, client.Connect(context.Background()), tt.kind, tt.msg)
			if c.cl != nil {
				t.Fatal("a failed Connect must close the connection")
			}
		})
	}
}

func TestStateAndReadOnlyChecksSendNothing(t *testing.T) {
	ctx := context.Background()
	client, _ := icloud(t, icloudLogin+`C: {t} SELECT "Shared"
S: {t} OK [READ-ONLY] SELECT completed
C: {t} EXAMINE INBOX
S: {t} NO [NONEXISTENT] Unknown
`+logout, live)
	must(t, client.Connect(ctx))
	_, err := client.UIDSearch(ctx, mail.SearchCriteria{})
	wantError(t, err, "error", "command SEARCH illegal in state AUTH, only allowed in states SELECTED")
	wantError(t, client.SelectRW(ctx, "Shared"), "readonly", `'"Shared"' is not writable`)
	wantError(t, client.Noop(ctx), "readonly", "mailbox status changed to READ-ONLY")
	wantError(t, client.Select(ctx, "INBOX"), "error", `IMAP command failed: NO [b'[NONEXISTENT] Unknown']`)
	_, err = client.UIDFetch(ctx, mail.UIDs(1), mail.FetchItems{})
	wantError(t, err, "error", "command FETCH illegal in state AUTH, only allowed in states SELECTED")
	if err := client.Select(ctx, "Caf\u00e9"); err == nil {
		t.Fatal("a non-ASCII mailbox name must fail before anything is sent")
	}
	client.Close()
}

func TestCopyUID(t *testing.T) {
	ctx := context.Background()
	client, _ := icloud(t, icloudLogin+`C: {t} SELECT INBOX
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 10:11 "Bills"
S: {t} OK [COPYUID 99 10:11 500:501] Done
C: {t} UID COPY 10 "Bills"
S: {t} OK done
C: {t} UID COPY 1 "A"
S: {t} OK [COPYUID 7 1 2] Done
C: {t} UID COPY 1 "A"
S: {t} OK done
C: {t} UID COPY 4,6 "A"
S: * OK [COPYUID 8 4,6 40:41] early
S: {t} OK [COPYUID 9 4,6 90:91] Done
`+logout, live)
	must(t, client.Connect(ctx))
	must(t, client.SelectRW(ctx, "INBOX"))
	steps := []struct {
		uids []uint32
		dest string
		want *mail.CopyUID
	}{
		{[]uint32{10, 11}, "Bills", &mail.CopyUID{UIDValidity: 99, Mapping: map[uint32]uint32{10: 500, 11: 501}}},
		{[]uint32{10}, "Bills", nil},
		{[]uint32{1}, "A", &mail.CopyUID{UIDValidity: 7, Mapping: map[uint32]uint32{1: 2}}},
		{[]uint32{1}, "A", nil},
		{[]uint32{4, 6}, "A", &mail.CopyUID{UIDValidity: 8, Mapping: map[uint32]uint32{4: 40, 6: 41}}},
	}
	for i, s := range steps {
		got, err := client.Copy(ctx, mail.UIDs(s.uids...), s.dest)
		if err != nil || !reflect.DeepEqual(got, s.want) {
			t.Fatalf("copy %d: got %+v, %v; want %+v", i, got, err, s.want)
		}
	}
	client.Close()
}

func TestStaleFetchResponsesJoinTheNextFetch(t *testing.T) {
	ctx := context.Background()
	client, _ := icloud(t, icloudLogin+`C: {t} EXAMINE INBOX
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} NOOP
S: * 7 FETCH (UID 107 FLAGS (\Seen))
S: * 8 FETCH (FLAGS (\Seen))
S: {t} OK NOOP completed
C: {t} UID FETCH 101 (UID FLAGS)
S: * 1 FETCH (UID 101 FLAGS ())
S: {t} OK FETCH completed
C: {t} UID FETCH 101 (UID FLAGS)
S: * 1 FETCH (UID 101 FLAGS ())
S: {t} OK FETCH completed
`+logout, live)
	must(t, client.Connect(ctx))
	must(t, client.Select(ctx, "INBOX"))
	must(t, client.Noop(ctx))
	first, err := client.UIDFetch(ctx, mail.UIDs(101), mail.FetchItems{})
	must(t, err)
	second, err := client.UIDFetch(ctx, mail.UIDs(101), mail.FetchItems{})
	must(t, err)
	if len(first) != 2 || first[0].UID != 107 || first[1].UID != 101 || len(second) != 1 {
		t.Fatalf("imaplib keeps unsolicited FETCH until the next UID command: %+v then %+v", first, second)
	}
	client.Close()
}

func TestSearchCriteria(t *testing.T) {
	ctx := context.Background()
	client, _ := icloud(t, icloudLogin+`C: {t} EXAMINE "Sent Messages"
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} UID SEARCH SINCE "1-Oct-2026"
S: * SEARCH 4 5
S: {t} OK SEARCH completed
C: {t} UID SEARCH HEADER "Message-ID" "<a@b.cc>"
S: * SEARCH 5
S: {t} OK SEARCH completed
C: {t} UID SEARCH UID 1:3,7
S: * SEARCH 1 7
S: * SEARCH 2
S: {t} OK SEARCH completed
C: {t} UID SEARCH ALL
S: {t} OK SEARCH completed
`+logout, live)
	must(t, client.Connect(ctx))
	must(t, client.Select(ctx, "Sent Messages"))
	set := mail.UIDs(1, 2, 3, 7)
	for _, tt := range []struct {
		criteria mail.SearchCriteria
		want     []uint32
	}{
		{mail.SearchCriteria{Since: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}, []uint32{4, 5}},
		{mail.SearchCriteria{HeaderName: "Message-ID", HeaderValue: "<a@b.cc>"}, []uint32{5}},
		{mail.SearchCriteria{UID: &set}, []uint32{1, 7}},
		{mail.SearchCriteria{}, []uint32{}},
	} {
		got, err := client.UIDSearch(ctx, tt.criteria)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("UIDSearch(%+v) = %v, %v; want %v", tt.criteria, got, err, tt.want)
		}
	}
	client.Close()
}

func TestStatusWithoutCondstore(t *testing.T) {
	ctx := context.Background()
	client, _ := icloud(t, `S: * OK [CAPABILITY IMAP4rev1] ready
C: {t} LOGIN "atrium.test@icloud.com" "app-specific password"
S: {t} OK [CAPABILITY IMAP4rev1 UIDPLUS] logged in
C/ ^{t} STATUS INBOX \((UIDVALIDITY|UIDNEXT|MESSAGES)( (UIDVALIDITY|UIDNEXT|MESSAGES)){2}\)$
S: * STATUS INBOX (MESSAGES 3 UIDNEXT 9 UIDVALIDITY 77)
S: {t} OK STATUS completed
C: {t} NOOP
S: {t} OK NOOP completed
`+logout, live)
	must(t, client.Connect(ctx))
	st, err := client.Status(ctx, "INBOX")
	if err != nil || st.HighestModSeq != nil || st.UIDValidity != 77 {
		t.Fatalf("Status = %+v, %v", st, err)
	}
	if client.HasCapability("IDLE") {
		t.Fatal("no IDLE advertised")
	}
	_, err = client.IdleChanges(ctx, time.Second)
	wantError(t, err, "error", "Server does not support IMAP4 IDLE")
	must(t, client.Noop(ctx))
	client.Close()
}

func TestIdle(t *testing.T) {
	ctx := context.Background()
	t.Run("change before the continuation", func(t *testing.T) {
		client, _ := icloud(t, icloudLogin+`C: {t} EXAMINE INBOX
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} IDLE
S: * 0 RECENT
S: + idling
S: * OK still here
C: DONE
S: * 3 EXISTS
S: {t} OK IDLE terminated
`+logout, live)
		must(t, client.Connect(ctx))
		must(t, client.Select(ctx, "INBOX"))
		start := time.Now()
		changed, err := client.IdleChanges(ctx, 100*time.Millisecond)
		if err != nil || !changed || time.Since(start) > time.Second {
			t.Fatalf("a RECENT before the continuation counts at once: %v, %v", changed, err)
		}
		client.Close()
	})
	t.Run("no change", func(t *testing.T) {
		client, _ := icloud(t, icloudLogin+`C: {t} EXAMINE INBOX
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} IDLE
S: + idling
S: * OK still here
C: DONE
S: {t} OK IDLE terminated
`+logout, live)
		must(t, client.Connect(ctx))
		must(t, client.Select(ctx, "INBOX"))
		changed, err := client.IdleChanges(ctx, 100*time.Millisecond)
		if err != nil || changed {
			t.Fatalf("IdleChanges = %v, %v; want false at the deadline", changed, err)
		}
		client.Close()
	})
	t.Run("denied", func(t *testing.T) {
		client, _ := icloud(t, icloudLogin+`C: {t} EXAMINE INBOX
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} IDLE
S: {t} NO not now
`+logout, live)
		must(t, client.Connect(ctx))
		must(t, client.Select(ctx, "INBOX"))
		_, err := client.IdleChanges(ctx, time.Second)
		wantError(t, err, "error", "idle denied: [b'not now']")
		client.Close()
	})
	t.Run("connection lost", func(t *testing.T) {
		client, _ := icloud(t, icloudLogin+`C: {t} EXAMINE INBOX
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} IDLE
S: + idling
! close
`, live)
		must(t, client.Connect(ctx))
		must(t, client.Select(ctx, "INBOX"))
		_, err := client.IdleChanges(ctx, time.Minute)
		wantError(t, err, "abort", "command: IDLE => socket error: EOF")
	})
	t.Run("context", func(t *testing.T) {
		client, _ := icloud(t, icloudLogin+`C: {t} EXAMINE INBOX
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} IDLE
S: + idling
! sleep 300ms
`, live)
		must(t, client.Connect(ctx))
		must(t, client.Select(ctx, "INBOX"))
		cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		if _, err := client.IdleChanges(cctx, time.Minute); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want the context's", err)
		}
	})
}

func TestReadTimeout(t *testing.T) {
	client, c := icloud(t, `S: {icloud.greeting}
C: {t} LOGIN "atrium.test@icloud.com" "app-specific password"
! sleep 500ms
`, live)
	c.readTimeout = 100 * time.Millisecond
	start := time.Now()
	wantError(t, client.Connect(context.Background()), "abort", "command: LOGIN => socket error: timed out")
	if time.Since(start) > 400*time.Millisecond {
		t.Fatalf("timed out after %v", time.Since(start))
	}
}
