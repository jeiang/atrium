package imapclient_test

import (
	"bufio"
	"net"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func TestGmailControlUnpatched(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		conn.Write([]byte("* OK [CAPABILITY IMAP4rev1 X-GM-EXT-1] Gimap ready\r\n"))
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		tag, _, _ := strings.Cut(line, " ")
		conn.Write([]byte("* 1 FETCH (UID 101 X-GM-LABELS (\\Inbox foo))\r\n" + tag + " OK Success\r\n"))
		br.ReadString('\n')
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c := imapclient.New(conn, nil)
	defer c.Close()
	_, err = c.Fetch(imap.UIDSetNum(101), &imap.FetchOptions{Flags: true}).Collect()
	const want = `unsupported msg-att name: "X-GM-LABELS"`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Fetch error = %v, want it to contain %s", err, want)
	}
}
