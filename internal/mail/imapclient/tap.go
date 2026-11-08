package imapclient

import (
	"bytes"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

type sentCommand struct {
	name string
	done bool
}

type sentEnd struct{ index, end int }

type tap struct {
	net.Conn

	mu    sync.Mutex
	sent  []sentCommand
	wline []byte
	wskip int
	wcont bool
	wcur  int
	ends  []sentEnd

	writeTimeout time.Duration
}

func newTap(c net.Conn, writeTimeout time.Duration) *tap {
	return &tap{Conn: c, writeTimeout: writeTimeout}
}

func (t *tap) Read(p []byte) (int, error) {
	return t.Conn.Read(p)
}

func (t *tap) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.ends = t.ends[:0]
	t.record(p)
	t.mu.Unlock()
	if t.writeTimeout > 0 {
		_ = t.SetWriteDeadline(time.Now().Add(t.writeTimeout))
	}
	n, err := t.Conn.Write(p)
	if t.writeTimeout > 0 {
		_ = t.SetWriteDeadline(time.Time{})
	}
	t.mu.Lock()
	for _, e := range t.ends {
		if e.end > n {
			t.sent[e.index].done = false
		}
	}
	t.mu.Unlock()
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		t.kill(errTimedOut)
	}
	return n, err
}

func (t *tap) record(p []byte) {
	off := 0
	for off < len(p) {
		if t.wskip > 0 {
			k := min(t.wskip, len(p)-off)
			t.wskip -= k
			off += k
			continue
		}
		i := bytes.IndexByte(p[off:], '\n')
		if i < 0 {
			t.wline = append(t.wline, p[off:]...)
			return
		}
		line := bytes.TrimSuffix(append(t.wline, p[off:off+i]...), []byte("\r"))
		off += i + 1
		if !t.wcont {
			t.wcur = -1
			if name, ok := commandTag(line); ok {
				t.sent = append(t.sent, sentCommand{name: name})
				t.wcur = len(t.sent) - 1
			}
		}
		t.wskip, t.wcont = clientLiteral(line)
		if !t.wcont && t.wcur >= 0 {
			t.sent[t.wcur].done = true
			t.ends = append(t.ends, sentEnd{t.wcur, off})
		}
		t.wline = t.wline[:0]
	}
}

func commandTag(line []byte) (string, bool) {
	sp := bytes.IndexByte(line, ' ')
	if sp < 2 || line[0] != 'T' {
		return "", false
	}
	for _, c := range line[1:sp] {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	name, _, _ := bytes.Cut(line[sp+1:], []byte(" "))
	return string(name), len(name) > 0
}

func clientLiteral(line []byte) (int, bool) {
	if !bytes.HasSuffix(line, []byte("}")) {
		return 0, false
	}
	open := bytes.LastIndexByte(line, '{')
	if open < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(string(line[open+1:len(line)-1]), "+"))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func (t *tap) sentLen() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.sent)
}

func (t *tap) written(name string, from int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.sent) - 1; i >= from; i-- {
		if t.sent[i].name == name {
			return t.sent[i].done
		}
	}
	return false
}

func (t *tap) kill(error) {
	_ = t.Close()
}
