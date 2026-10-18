package imapclient_test

// Part of atrium's local Gmail patch; not in upstream go-imap.

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type scriptStep struct {
	server bool
	line   string         // server line, {t} substituted at send time
	match  *regexp.Regexp // client line; group 1 is the tag when the line has {t}
}

func loadScript(t *testing.T, path string) []scriptStep {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var steps []scriptStep
	for n, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "S: "):
			steps = append(steps, scriptStep{server: true, line: line[3:]})
		case strings.HasPrefix(line, "C: "):
			pattern := "^" + regexp.QuoteMeta(line[3:]) + "$"
			pattern = strings.ReplaceAll(pattern, regexp.QuoteMeta("{t}"), `(\S+)`)
			steps = append(steps, scriptStep{match: regexp.MustCompile(pattern)})
		case strings.HasPrefix(line, "C/ "):
			pattern := strings.ReplaceAll(line[3:], "{t}", `(\S+)`)
			steps = append(steps, scriptStep{match: regexp.MustCompile(pattern)})
		default:
			t.Fatalf("%v:%v: unknown script line %q", path, n+1, line)
		}
	}
	return steps
}

// serveScript replays steps on conn and reports the first mismatch.
func serveScript(conn net.Conn, steps []scriptStep) error {
	defer conn.Close()
	br := bufio.NewReader(conn)
	tag := ""
	for i, step := range steps {
		if step.server {
			line := strings.ReplaceAll(step.line, "{t}", tag)
			if _, err := conn.Write([]byte(line + "\r\n")); err != nil {
				return fmt.Errorf("step %v: write %q: %v", i, line, err)
			}
			continue
		}
		line, err := br.ReadString('\n')
		if err != nil {
			return fmt.Errorf("step %v: want client line matching %q, read: %v", i, step.match, err)
		}
		line = strings.TrimSuffix(line, "\r\n")
		m := step.match.FindStringSubmatch(line)
		if m == nil {
			return fmt.Errorf("step %v: client sent %q, want match for %q", i, line, step.match)
		}
		if len(m) > 1 {
			tag = m[1]
		}
	}
	return nil
}

func TestGmailTranscript(t *testing.T) {
	steps := loadScript(t, "testdata/gmail/session.txt")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		serverErr <- serveScript(conn, steps)
	}()

	unilateral := make(chan *imapclient.FetchMessageBuffer, 1)
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c := imapclient.New(conn, &imapclient.Options{
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
	defer c.Close()

	fail := func(format string, args ...interface{}) {
		t.Helper()
		c.Close()
		if err := <-serverErr; err != nil {
			t.Errorf("server: %v", err)
		}
		t.Fatalf(format, args...)
	}

	if !c.Caps().Has(imap.CapGmailExt1) {
		fail("X-GM-EXT-1 not advertised")
	}
	if err := c.Login("atrium.test@gmail.com", "app password").Wait(); err != nil {
		fail("Login: %v", err)
	}
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		fail("Select: %v", err)
	}

	msgs, err := c.Fetch(imap.UIDSetNum(101, 102, 103, 104), &imap.FetchOptions{
		Flags: true,
		Gmail: imap.FetchGmailOptions{MsgID: true, ThrID: true, Labels: true},
	}).Collect()
	if err != nil {
		fail("Fetch: %v", err)
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
		fail("Fetch:\n got %#v\nwant %#v", got, want)
	}

	msgs, err = c.Fetch(imap.UIDSetNum(101), &imap.FetchOptions{
		Gmail: imap.FetchGmailOptions{Labels: true},
	}).Collect()
	if err != nil {
		fail("Fetch with unknown items: %v", err)
	}
	if len(msgs) != 1 || msgs[0].UID != 101 || !reflect.DeepEqual(msgs[0].GmailLabels, []string{"q"}) ||
		!reflect.DeepEqual(msgs[0].Flags, []imap.Flag{imap.FlagSeen, imap.FlagFlagged}) {
		fail("Fetch with unknown items: got %#v", msgs)
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
			fail("StoreLabels #%v: %v", i, err)
		}
		if s.want == nil {
			if len(msgs) != 0 {
				fail("StoreLabels #%v: silent store returned %#v", i, msgs)
			}
			continue
		}
		if len(msgs) != 1 || !reflect.DeepEqual(msgs[0].GmailLabels, s.want) {
			fail("StoreLabels #%v: got %#v, want labels %q", i, msgs, s.want)
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
			fail("UIDSearch #%v: %v", i, err)
		}
		if got := data.AllUIDs(); !reflect.DeepEqual(got, s.want) {
			fail("UIDSearch #%v: got %v, want %v", i, got, s.want)
		}
	}

	if err := c.Noop().Wait(); err != nil {
		fail("Noop: %v", err)
	}
	select {
	case buf := <-unilateral:
		if buf.SeqNum != 2 || !reflect.DeepEqual(buf.GmailLabels, []string{"foo", "bar"}) ||
			!reflect.DeepEqual(buf.Flags, []imap.Flag{imap.FlagSeen}) {
			fail("unilateral FETCH: got %#v", buf)
		}
	case <-time.After(5 * time.Second):
		fail("unilateral FETCH not delivered")
	}

	if err := c.Logout().Wait(); err != nil {
		fail("Logout: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server: %v", err)
	}
}

func TestGmailSearchCriteriaAnd(t *testing.T) {
	criteria := imap.SearchCriteria{Gmail: imap.SearchCriteriaGmail{Raw: []string{"a"}, Labels: []string{"x"}}}
	criteria.And(&imap.SearchCriteria{Gmail: imap.SearchCriteriaGmail{Raw: []string{"b"}, MsgID: 7, ThrID: 9, Labels: []string{"y"}}})
	want := imap.SearchCriteriaGmail{Raw: []string{"a", "b"}, MsgID: 7, ThrID: 9, Labels: []string{"x", "y"}}
	if !reflect.DeepEqual(criteria.Gmail, want) {
		t.Errorf("And: got %#v, want %#v", criteria.Gmail, want)
	}
}
