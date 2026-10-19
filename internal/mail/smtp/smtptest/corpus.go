package smtptest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/mail/smtp"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

// Record is one line of testdata/corpora/smtp.jsonl.
type Record struct {
	Fn          string          `json:"fn"`
	Case        string          `json:"case"`
	Input       json.RawMessage `json:"input"`
	Output      json.RawMessage `json:"output"`
	Error       *PyError        `json:"error"`
	Account     string          `json:"account"`
	User        string          `json:"user"`
	Refused     json.RawMessage `json:"refused_by_server"`
	Failure     *Failure        `json:"failure"`
	Calls       json.RawMessage `json:"calls"`
	Row         json.RawMessage `json:"row"`
	SentMailbox *string         `json:"sent_mailbox"`
	AppendedTo  []string        `json:"appended_to"`
	AppendError string          `json:"append_error"`
	AsBytes     []byte          `json:"as_bytes"`
	AsBytesUTF8 []byte          `json:"as_bytes_utf8"`
}

type PyError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Failure is the stage a fake server fails at and the Python exception it raises there.
type Failure struct {
	Stage string            `json:"stage"`
	Type  string            `json:"type"`
	Args  []json.RawMessage `json:"args"`
}

type draft struct {
	Body      string `json:"body"`
	CcAddrs   string `json:"cc_addrs"`
	InReplyTo string `json:"in_reply_to"`
	Refs      string `json:"refs"`
	Subject   string `json:"subject"`
	ToAddrs   string `json:"to_addrs"`
}

// Corpus is the SMTP corpus with the draft of every case by name and the pinned clock.
type Corpus struct {
	Records []Record
	Drafts  map[string]smtp.Fields
	Now     float64
}

// LoadCorpus reads testdata/corpora/smtp.jsonl. The unconfigured cases send the drafts the generator gave them.
func LoadCorpus(t testing.TB) Corpus {
	t.Helper()
	f, err := os.Open(filepath.Join(golden.Dir(), "..", "corpora", "smtp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	c := Corpus{Drafts: map[string]smtp.Fields{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<24)
	for sc.Scan() {
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Fn == "build_message" {
			var in []json.RawMessage
			var d draft
			if err := json.Unmarshal(r.Input, &in); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(in[1], &d); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(in[2], &c.Now); err != nil {
				t.Fatal(err)
			}
			c.Drafts[r.Case] = smtp.Fields{ToAddrs: d.ToAddrs, CcAddrs: d.CcAddrs, Subject: d.Subject, Body: d.Body, InReplyTo: d.InReplyTo, Refs: d.Refs}
		}
		c.Records = append(c.Records, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	c.Drafts["unconfigured_icloud"] = c.Drafts["icloud_short_user_reply"]
	c.Drafts["unconfigured_gmail"] = c.Drafts["gmail_plain"]
	c.Drafts["icloud_no_sent_mailbox"] = c.Drafts["icloud_short_user_reply"]
	c.Drafts["icloud_append_fails"] = c.Drafts["icloud_short_user_reply"]
	return c
}

// Refusals decodes a JSON object of addr -> [code, text], keeping its key order.
func Refusals(t testing.TB, raw json.RawMessage) smtp.Refusals {
	t.Helper()
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	var out smtp.Refusals
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		var reply []json.RawMessage
		if err := dec.Decode(&reply); err != nil {
			t.Fatal(err)
		}
		r := smtp.Refusal{Addr: key.(string)}
		if err := json.Unmarshal(reply[0], &r.Code); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(reply[1], &r.Msg); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// Exception is the Go error that stands in for the Python exception of a failure record.
func Exception(t testing.TB, f *Failure) error {
	t.Helper()
	str := func(i int) string {
		var s string
		if err := json.Unmarshal(f.Args[i], &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	num := func(i int) int {
		var n int
		if err := json.Unmarshal(f.Args[i], &n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	switch f.Type {
	case "ConnectionRefusedError", "gaierror", "BrokenPipeError", "ConnectionResetError":
		return &py.OSError{Kind: f.Type, Errno: num(0), HasErrno: true, Strerror: str(1)}
	case "TimeoutError":
		return &py.OSError{Kind: f.Type, Strerror: str(0)}
	case "SSLCertVerificationError":
		return errors.New(str(1))
	case "SMTPConnectError", "SMTPResponseException", "SMTPAuthenticationError", "SMTPHeloError", "SMTPDataError":
		return &smtp.ResponseError{Kind: f.Type, Code: num(0), Msg: str(1)}
	case "SMTPServerDisconnected", "SMTPNotSupportedError", "SMTPException":
		return &smtp.Failure{Kind: f.Type, Msg: str(0)}
	case "SMTPSenderRefused":
		return &smtp.SenderRefused{Code: num(0), Msg: str(1), Sender: str(2)}
	case "SMTPRecipientsRefused":
		return &smtp.RecipientsRefused{Refused: Refusals(t, f.Args[0])}
	}
	t.Fatalf("no Go error for %s", f.Type)
	return nil
}

// NewServer is the record's fake server: its refusal map and its failure, if any.
func NewServer(t testing.TB, r Record) *Server {
	t.Helper()
	s := &Server{Refused: Refusals(t, r.Refused)}
	if r.Failure != nil {
		err := Exception(t, r.Failure)
		stage := r.Failure.Stage
		s.Fail = func(at string) error {
			if at == stage {
				return err
			}
			return nil
		}
	}
	return s
}

// PinnedMsgID is the generator's make_msgid: <golden.0001@domain> for the sender's domain.
func PinnedMsgID(domain string) string { return "<golden.0001@" + domain + ">" }

// SenderDomain is sender.rsplit("@", 1)[-1].
func SenderDomain(sender string) string { return sender[strings.LastIndex(sender, "@")+1:] }

// RefusalMap is the refusal dict as the corpus records it: addr -> str((code, reply)).
func RefusalMap(rs smtp.Refusals) map[string]string {
	out := map[string]string{}
	for _, r := range rs {
		out[r.Addr] = r.Reply()
	}
	return out
}
