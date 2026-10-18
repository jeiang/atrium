package imapscript_test

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/jeiang/atrium/internal/testutil/imapscript"
)

func TestGmailTranscript(t *testing.T) {
	srv := imapscript.Serve(t, imapscript.Load(t, "testdata/gmail/session.txt"))
	unilateral := make(chan *imapclient.FetchMessageBuffer, 1)
	c := srv.Dial(t, &imapclient.Options{
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Fetch: func(msg *imapclient.FetchMessageData) {
				buf, err := msg.Collect()
				if err != nil {
					t.Errorf("unilateral FETCH: %v", err)
				}
				unilateral <- buf
			},
		},
	})

	if !c.Caps().Has(imap.CapGmailExt1) {
		t.Fatal("X-GM-EXT-1 not advertised")
	}
	if err := c.Login("atrium.test@gmail.com", "app password").Wait(); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatalf("Select: %v", err)
	}

	msgs, err := c.Fetch(imap.UIDSetNum(101, 102, 103, 104), &imap.FetchOptions{
		Flags: true,
		Gmail: imap.FetchGmailOptions{MsgID: true, ThrID: true, Labels: true},
	}).Collect()
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	type gmailMsg struct {
		UID    imap.UID
		MsgID  uint64
		ThrID  uint64
		Labels []string
		Flags  []imap.Flag
	}
	var got []gmailMsg
	for _, m := range msgs {
		got = append(got, gmailMsg{m.UID, m.GmailMsgID, m.GmailThrID, m.GmailLabels, m.Flags})
	}
	want := []gmailMsg{
		{101, 1278455344230334865, 1278455344230334865, []string{`\Inbox`, `\Sent`, "Important"}, []imap.Flag{imap.FlagSeen}},
		{102, 17000000000000000001, 1266894439832287888, []string{"Muy Importante", `\Inbox`, "[Gmail]/Trash"}, nil},
		{103, 18446744073709551615, 18446744073709551615, []string{"été"}, []imap.Flag{imap.FlagSeen, imap.FlagFlagged}},
		{104, 1, 1, []string{}, nil},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Fetch:\n got %#v\nwant %#v", got, want)
	}

	msgs, err = c.Fetch(imap.UIDSetNum(101), &imap.FetchOptions{
		Gmail: imap.FetchGmailOptions{Labels: true},
	}).Collect()
	if err != nil {
		t.Fatalf("Fetch with unknown items: %v", err)
	}
	if len(msgs) != 1 || msgs[0].UID != 101 || !reflect.DeepEqual(msgs[0].GmailLabels, []string{"q"}) ||
		!reflect.DeepEqual(msgs[0].Flags, []imap.Flag{imap.FlagSeen, imap.FlagFlagged}) {
		t.Fatalf("Fetch with unknown items: got %#v", msgs)
	}

	stores := []struct {
		store *imap.StoreLabels
		set   imap.UIDSet
		want  []string
	}{
		{&imap.StoreLabels{Op: imap.StoreFlagsDel, Labels: []string{`\Inbox`}}, imap.UIDSetNum(101), []string{`\Sent`, "Important"}},
		{&imap.StoreLabels{Op: imap.StoreFlagsAdd, Labels: []string{"Muy Importante", "été", `Back\slash`}}, imap.UIDSetNum(101),
			[]string{`\Sent`, "Important", "Muy Importante", "été", `Back\slash`}},
		{&imap.StoreLabels{Op: imap.StoreFlagsAdd, Silent: true, Labels: []string{`\Starred`}}, imap.UIDSetNum(101, 102), nil},
		{&imap.StoreLabels{Op: imap.StoreFlagsSet, Labels: []string{}}, imap.UIDSetNum(101), []string{}},
	}
	for i, s := range stores {
		msgs, err := c.StoreLabels(s.set, s.store).Collect()
		if err != nil {
			t.Fatalf("StoreLabels #%v: %v", i, err)
		}
		if s.want == nil {
			if len(msgs) != 0 {
				t.Fatalf("StoreLabels #%v: silent store returned %#v", i, msgs)
			}
			continue
		}
		if len(msgs) != 1 || !reflect.DeepEqual(msgs[0].GmailLabels, s.want) {
			t.Fatalf("StoreLabels #%v: got %#v, want labels %q", i, msgs, s.want)
		}
	}

	searches := []struct {
		criteria *imap.SearchCriteria
		want     []imap.UID
	}{
		{&imap.SearchCriteria{Gmail: imap.SearchCriteriaGmail{Raw: []string{"has:attachment in:unread"}}}, []imap.UID{101, 103}},
		{&imap.SearchCriteria{
			NotFlag: []imap.Flag{imap.FlagSeen},
			Gmail: imap.SearchCriteriaGmail{
				Raw:    []string{"category:promotions"},
				MsgID:  1278455344230334865,
				ThrID:  1266894439832287888,
				Labels: []string{"Muy Importante"},
			},
		}, []imap.UID{102}},
	}
	for i, s := range searches {
		data, err := c.UIDSearch(s.criteria, nil).Wait()
		if err != nil {
			t.Fatalf("UIDSearch #%v: %v", i, err)
		}
		if got := data.AllUIDs(); !reflect.DeepEqual(got, s.want) {
			t.Fatalf("UIDSearch #%v: got %v, want %v", i, got, s.want)
		}
	}

	if err := c.Noop().Wait(); err != nil {
		t.Fatalf("Noop: %v", err)
	}
	select {
	case buf := <-unilateral:
		if buf.SeqNum != 2 || !reflect.DeepEqual(buf.GmailLabels, []string{"foo", "bar"}) ||
			!reflect.DeepEqual(buf.Flags, []imap.Flag{imap.FlagSeen}) {
			t.Fatalf("unilateral FETCH: got %#v", buf)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unilateral FETCH not delivered")
	}

	if err := c.Logout().Wait(); err != nil {
		t.Fatalf("Logout: %v", err)
	}
}

func TestGmailRawSearch(t *testing.T) {
	srv := imapscript.Serve(t, imapscript.Load(t, "testdata/gmail/xgmraw.txt"))
	c := srv.Dial(t, nil)
	if c.Caps().Has(imap.CapLiteralMinus) {
		t.Fatal("LITERAL- advertised before LOGIN")
	}
	if err := c.Login("atrium.test@gmail.com", "app password").Wait(); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatalf("Select: %v", err)
	}

	search := func(want []imap.UID, raw ...string) {
		t.Helper()
		data, err := c.UIDSearch(&imap.SearchCriteria{Gmail: imap.SearchCriteriaGmail{Raw: raw}}, nil).Wait()
		if err != nil {
			t.Fatalf("UIDSearch %q: %v", raw, err)
		}
		if got := data.AllUIDs(); !slices.Equal(got, want) {
			t.Fatalf("UIDSearch %q: got %v, want %v", raw, got, want)
		}
	}
	search([]imap.UID{101}, `from:"Jane Doe" subject:(a\b) in:anywhere`)
	search(nil, "")
	search([]imap.UID{103}, "subject:été")
	search([]imap.UID{102, 103}, `from:"José Ñúñez" 東京 -in:chats`)
	search(nil, "has:attachment", "label:Été", "older_than:1y")

	if _, err := c.Enable(imap.CapUTF8Accept).Wait(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	search([]imap.UID{102}, `from:"José Ñúñez" 東京`)

	if err := c.Logout().Wait(); err != nil {
		t.Fatalf("Logout: %v", err)
	}
}
