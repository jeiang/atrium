package imapscript_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/jeiang/atrium/internal/testutil/imapscript"
)

const appendLiteral = "From: Atrium Test <atrium.test@example.invalid>\r\n" +
	"To: Atrium Test <atrium.test@example.invalid>\r\n" +
	"Subject: atrium transcript test\r\n" +
	"Message-ID: <atrium-transcript-test-408@atrium.invalid>\r\n" +
	"Date: Sat, 24 Oct 2026 12:00:00 +0000\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Synthetic message for the atrium IMAP transcript test.\r\n"

func TestICloudLiveSession(t *testing.T) {
	srv := imapscript.Serve(t, imapscript.Load(t, "testdata/icloud/live.txt"))
	c := srv.Dial(t, nil)

	if _, err := c.Capability().Wait(); err != nil {
		t.Fatalf("Capability: %v", err)
	}
	if err := c.Login("atrium.test@icloud.com", "app password").Wait(); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := c.List("", "", nil).Collect(); err != nil {
		t.Fatalf("List root: %v", err)
	}
	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true, CondStore: true}).Wait(); err != nil {
		t.Fatalf("Examine INBOX: %v", err)
	}
	msgs, err := c.Fetch(imap.UIDSet{{Start: 1, Stop: 0}}, &imap.FetchOptions{
		UID: true, Flags: true, ChangedSince: 96216,
	}).Collect()
	if err != nil {
		t.Fatalf("Fetch CHANGEDSINCE: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("Fetch CHANGEDSINCE: got %d messages, want none", len(msgs))
	}
	idle, err := c.Idle()
	if err != nil {
		t.Fatalf("Idle: %v", err)
	}
	if err := idle.Close(); err != nil {
		t.Fatalf("Idle close: %v", err)
	}
	if err := idle.Wait(); err != nil {
		t.Fatalf("Idle: %v", err)
	}
	if boxes, err := c.List("", "atrium-transcript-test*", nil).Collect(); err != nil || len(boxes) != 0 {
		t.Fatalf("List before create: %v %v", boxes, err)
	}
	if err := c.Create("atrium-transcript-test", nil).Wait(); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := c.Create("atrium-transcript-test-2", nil).Wait(); err != nil {
		t.Fatalf("Create second: %v", err)
	}
	app := c.Append("atrium-transcript-test", int64(len(appendLiteral)), &imap.AppendOptions{
		Flags: []imap.Flag{imap.FlagSeen},
	})
	if _, err := app.Write([]byte(appendLiteral)); err != nil {
		t.Fatalf("Append write: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("Append close: %v", err)
	}
	appended, err := app.Wait()
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if appended.UIDValidity != 1750835959 || appended.UID != 1 {
		t.Fatalf("APPENDUID: got %#v", appended)
	}
	if _, err := c.Select("atrium-transcript-test", nil).Wait(); err != nil {
		t.Fatalf("Select: %v", err)
	}
	found, err := c.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
	if err != nil {
		t.Fatalf("UID SEARCH: %v", err)
	}
	if got := found.AllUIDs(); !reflect.DeepEqual(got, []imap.UID{1}) {
		t.Fatalf("UID SEARCH: got %v", got)
	}
	copied, err := c.Copy(imap.UIDSetNum(1), "atrium-transcript-test-2").Wait()
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if copied.UIDValidity != 1750835961 || copied.SourceUIDs.String() != "1" || copied.DestUIDs.String() != "1" {
		t.Fatalf("COPYUID: got %#v", copied)
	}
	if _, err := c.Select("atrium-transcript-test-2", nil).Wait(); err != nil {
		t.Fatalf("Select copy: %v", err)
	}
	if _, err := c.UIDSearch(&imap.SearchCriteria{}, nil).Wait(); err != nil {
		t.Fatalf("UID SEARCH copy: %v", err)
	}
	if err := c.Store(imap.UIDSetNum(1), &imap.StoreFlags{
		Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted},
	}, nil).Close(); err != nil {
		t.Fatalf("Store \\Deleted: %v", err)
	}
	expunged, err := c.UIDExpunge(imap.UIDSetNum(1)).Collect()
	if err != nil {
		t.Fatalf("UID EXPUNGE: %v", err)
	}
	if !reflect.DeepEqual(expunged, []uint32{1}) {
		t.Fatalf("UID EXPUNGE: got %v, want the plain EXPUNGE sequence number", expunged)
	}
	if found, err := c.UIDSearch(&imap.SearchCriteria{}, nil).Wait(); err != nil {
		t.Fatalf("UID SEARCH after expunge: %v", err)
	} else if got := found.AllUIDs(); len(got) != 0 {
		t.Fatalf("UID SEARCH after expunge: got %v", got)
	}
	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatalf("Examine INBOX again: %v", err)
	}
	if err := c.Delete("atrium-transcript-test").Wait(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := c.Delete("atrium-transcript-test-2").Wait(); err != nil {
		t.Fatalf("Delete second: %v", err)
	}
	if boxes, err := c.List("", "atrium-transcript-test*", nil).Collect(); err != nil || len(boxes) != 0 {
		t.Fatalf("List after delete: %v %v", boxes, err)
	}
	if err := c.Logout().Wait(); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	c = srv.Dial(t, nil)
	if _, err := c.Capability().Wait(); err != nil {
		t.Fatalf("Capability fetch: %v", err)
	}
	if err := c.Login("atrium.test@icloud.com", "app password").Wait(); err != nil {
		t.Fatalf("Login fetch: %v", err)
	}
	if _, err := c.List("", "", nil).Collect(); err != nil {
		t.Fatalf("List root fetch: %v", err)
	}
	if _, err := c.Select("Archive", &imap.SelectOptions{ReadOnly: true, CondStore: true}).Wait(); err != nil {
		t.Fatalf("Examine Archive: %v", err)
	}
	msgs, err = c.Fetch(imap.UIDSet{{Start: 357, Stop: 0}}, &imap.FetchOptions{
		UID: true, Flags: true, ChangedSince: 95714,
	}).Collect()
	if err != nil {
		t.Fatalf("Fetch Archive: %v", err)
	}
	if len(msgs) != 6 || msgs[0].UID != 357 || msgs[5].UID != 362 {
		t.Fatalf("Fetch Archive: got %d messages", len(msgs))
	}
	idle, err = c.Idle()
	if err != nil {
		t.Fatalf("Idle fetch: %v", err)
	}
	if err := idle.Close(); err != nil {
		t.Fatalf("Idle close fetch: %v", err)
	}
	if err := idle.Wait(); err != nil {
		t.Fatalf("Idle fetch: %v", err)
	}
	if err := c.Logout().Wait(); err != nil {
		t.Fatalf("Logout fetch: %v", err)
	}
}

func TestGmailLiveRead(t *testing.T) {
	srv := imapscript.Serve(t, imapscript.Load(t, "testdata/gmail/live.txt"))
	c := srv.Dial(t, nil)

	if c.Caps() == nil {
		t.Fatal("Capability: no capabilities")
	}
	if err := c.Login("atrium.test@gmail.com", "app password").Wait(); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := c.List("", "", nil).Collect(); err != nil {
		t.Fatalf("List root: %v", err)
	}
	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatalf("Examine INBOX: %v", err)
	}
	for _, name := range []string{"atrium-transcript-test", "atrium-transcript-test-2"} {
		err := c.Delete(name).Wait()
		if err == nil || !strings.Contains(err.Error(), "NONEXISTENT") {
			t.Fatalf("Delete %s: got %v, want NONEXISTENT", name, err)
		}
	}
	if err := c.Logout().Wait(); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	c = srv.Dial(t, nil)
	if c.Caps() == nil {
		t.Fatal("Capability fetch: no capabilities")
	}
	if err := c.Login("atrium.test@gmail.com", "app password").Wait(); err != nil {
		t.Fatalf("Login fetch: %v", err)
	}
	if _, err := c.List("", "", nil).Collect(); err != nil {
		t.Fatalf("List root fetch: %v", err)
	}
	if _, err := c.Select("[Gmail]/All Mail", &imap.SelectOptions{ReadOnly: true, CondStore: true}).Wait(); err != nil {
		t.Fatalf("Examine All Mail: %v", err)
	}
	msgs, err := c.Fetch(imap.UIDSet{{Start: 52947, Stop: 0}}, &imap.FetchOptions{
		UID: true, Flags: true,
		Gmail:        imap.FetchGmailOptions{MsgID: true, ThrID: true, Labels: true},
		ChangedSince: 39698551,
	}).Collect()
	if err != nil {
		t.Fatalf("Fetch All Mail: %v", err)
	}
	if len(msgs) != 1 || msgs[0].UID != 52952 || msgs[0].GmailMsgID != 1000000000000000001 {
		t.Fatalf("Fetch All Mail: got %#v", msgs)
	}
	idle, err := c.Idle()
	if err != nil {
		t.Fatalf("Idle: %v", err)
	}
	if err := idle.Close(); err != nil {
		t.Fatalf("Idle close: %v", err)
	}
	if err := idle.Wait(); err != nil {
		t.Fatalf("Idle: %v", err)
	}
	if err := c.Logout().Wait(); err != nil {
		t.Fatalf("Logout fetch: %v", err)
	}
}
