package imapscript_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/jeiang/atrium/internal/testutil/imapscript"
)

func TestICloudSession(t *testing.T) {
	srv := imapscript.Serve(t, imapscript.Load(t, "testdata/icloud/session.txt"))
	exists := make(chan uint32, 1)
	c := srv.Dial(t, &imapclient.Options{
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(data *imapclient.UnilateralDataMailbox) {
				if data.NumMessages != nil {
					exists <- *data.NumMessages
				}
			},
		},
	})

	if caps := c.Caps(); !caps.Has(imap.CapIMAP4rev1) || caps.Has(imap.CapCondStore) {
		t.Fatalf("greeting caps: %v", caps)
	}
	if err := c.Login("atrium.test@icloud.com", "app-specific password").Wait(); err != nil {
		t.Fatalf("Login: %v", err)
	}
	for _, cap := range []imap.Cap{imap.CapCondStore, imap.CapQResync, imap.CapUIDPlus, imap.CapIdle, imap.CapLiteralPlus} {
		if !c.Caps().Has(cap) {
			t.Fatalf("%s not advertised after LOGIN", cap)
		}
	}
	sel, err := c.Select("INBOX", &imap.SelectOptions{CondStore: true}).Wait()
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if sel.HighestModSeq != 81250 || sel.UIDValidity != 1544650101 || sel.NumMessages != 3 {
		t.Fatalf("Select: got %#v", sel)
	}

	msgs, err := c.Fetch(imap.UIDSet{{Start: 1, Stop: 0}}, &imap.FetchOptions{
		UID: true, Flags: true, ModSeq: true, ChangedSince: 81200,
	}).Collect()
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	type change struct {
		UID    imap.UID
		ModSeq uint64
		Flags  []imap.Flag
	}
	var got []change
	for _, m := range msgs {
		got = append(got, change{m.UID, m.ModSeq, m.Flags})
	}
	want := []change{{302, 81230, []imap.Flag{imap.FlagSeen}}, {303, 81250, []imap.Flag{imap.FlagSeen, imap.FlagFlagged}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Fetch CHANGEDSINCE:\n got %#v\nwant %#v", got, want)
	}

	moved := imap.UIDSetNum(301, 302)
	copied, err := c.Copy(moved, "Archive").Wait()
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if copied.UIDValidity != 1544650202 || copied.SourceUIDs.String() != "301:302" || copied.DestUIDs.String() != "900:901" {
		t.Fatalf("COPYUID: got %#v", copied)
	}
	if err := c.Store(moved, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		t.Fatalf("Store: %v", err)
	}
	expunged, err := c.UIDExpunge(moved).Collect()
	if err != nil {
		t.Fatalf("UIDExpunge: %v", err)
	}
	if !reflect.DeepEqual(expunged, []uint32{1, 1}) {
		t.Fatalf("UIDExpunge: got %v", expunged)
	}

	idle, err := c.Idle()
	if err != nil {
		t.Fatalf("Idle: %v", err)
	}
	select {
	case n := <-exists:
		if n != 2 {
			t.Fatalf("EXISTS during IDLE: got %d", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EXISTS during IDLE not delivered")
	}
	if err := idle.Close(); err != nil {
		t.Fatalf("Idle close: %v", err)
	}
	if err := idle.Wait(); err != nil {
		t.Fatalf("Idle: %v", err)
	}

	if err := c.Noop().Wait(); err == nil {
		t.Fatal("Noop on a dropped connection succeeded")
	}

	c = srv.Dial(t, nil)
	if err := c.Login("atrium.test@icloud.com", "app-specific password").Wait(); err == nil {
		t.Fatal("Login: rejected credentials succeeded")
	}
	if err := c.Login("atrium.test@icloud.com", "app-specific password").Wait(); err != nil {
		t.Fatalf("Login after reconnect: %v", err)
	}
	if err := c.Logout().Wait(); err != nil {
		t.Fatalf("Logout: %v", err)
	}
}
