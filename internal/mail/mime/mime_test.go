package mime

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestHeaderValue(t *testing.T) {
	for _, c := range []struct {
		name, raw, want string
		raises          bool
	}{
		{"From", "=?utf-8?q?Last=2C_First?= <a@b.test>", `"Last, First" <a@b.test>`, false},
		{"From", "\xc3\xa9mile <e@x.test>", "émile <e@x.test>", false},
		{"From", "\xe9mile <e@x.test>", "\ufffdmile <e@x.test>", false},
		{"To", "Team: a@x.test, B <b@x.test>;, c@x.test", "Team: a@x.test, B <b@x.test>;, c@x.test", false},
		{"To", "<>", "<>", false},
		{"To", "Just A Name", `"Just A Name"`, false},
		{"Cc", `=?utf-8?q?bad=FF?= <"q u"@b> , :: x@d`, "", true},
		{"Cc", "=?utf-8?q?a=0Ab?= <x@y>", "", true},
		{"Subject", "=?utf-8?q?a?= =?utf-8?q?b?=  c=?utf-8?q?d?=", "ab  cd", false},
		{"Subject", "A very long subject\r\n line", "A very long subject line", false},
		{"Message-ID", "(c) <a@b> x", "(c) <a@b> ", false},
		{"Message-ID", " < a@b > ", "< a@b> ", false},
		{"In-Reply-To", "<a@[ 1.2]>", "", true},
		{"References", "<a@b>,<c@d> junk", "<a@b> <c@d> junk", false},
		{"Content-Type", `text/plain; name="=?utf-8?B?w6k=?="; charset="utf-8" (c)`, `text/plain; name="é"; charset="utf-8"`, false},
		{"Content-Type", "text/plain; name*0*=utf-8''a%C3%A9; name*1=b", `text/plain; name="aéb"`, false},
		{"Content-Disposition", "attachment; filename==?utf-8?B?w6kx?=", "attachment;", false},
		{"MIME-Version", "1.0 (c\xc3\xa9)", "1.0 (cé)", false},
		{"Date", "6 Oct 26 10:00 EST", "Tue, 06 Oct 2026 10:00:00 -0500", false},
		{"Date", "not a date \xc3\xa9", "not a date é", false},
	} {
		msg, err := Parse([]byte(c.name + ": " + c.raw + "\r\n\r\n"))
		if err != nil {
			t.Fatal(err)
		}
		got, ok, err := msg.Get(c.name)
		if !ok || (err != nil) != c.raises || got != c.want {
			t.Errorf("%s: %q = %q, %v, %v; want %q, raises %v", c.name, c.raw, got, ok, err, c.want, c.raises)
		}
	}
}

func TestAddresses(t *testing.T) {
	for _, c := range []struct {
		in    string
		one   Addr
		lists []Addr
	}{
		{`"Last, First" <a@b>`, Addr{"Last, First", "a@b"}, []Addr{{"Last, First", "a@b"}}},
		{"Just A Name", Addr{"", "Just"}, []Addr{{"", "Just"}}},
		{"(unbalanced <a@b>", Addr{}, []Addr{{}}},
		{"a@[1.2.3.4]", Addr{}, []Addr{{}}},
		{"a@b, c@d", Addr{}, []Addr{{"", "a@b"}, {"", "c@d"}}},
		{"Team: a@b, c@d;", Addr{}, []Addr{{"", "a@b"}, {"", "c@d"}}},
		{`"q\"x" <a@b>`, Addr{`q"x`, "a@b"}, []Addr{{`q"x`, "a@b"}}},
	} {
		if got, err := ParseAddr(c.in); err != nil || got != c.one {
			t.Errorf("ParseAddr(%q) = %+v, %v, want %+v", c.in, got, err, c.one)
		}
		if got, err := GetAddresses([]string{c.in}); err != nil || !reflect.DeepEqual(got, c.lists) {
			t.Errorf("GetAddresses(%q) = %+v, %v, want %+v", c.in, got, err, c.lists)
		}
	}
}

func TestParseDate(t *testing.T) {
	for in, want := range map[string]float64{
		"Tue, 06 Oct 2026 10:00:00 -0000": 1791280800,
		"6 Oct 26 10:00 EST":              1791298800,
		"6 Oct ２０２６ 10:00:00 +0100":       1791277200,
		"06-Oct-2026 10:00:00":            -1,
		"Tue, 31 Feb 2026 10:00:00 +0000": -1,
		"6 Oct 2026 10:00 +2400":          -1,
		"6 ıct 2026 10:00 eſt":            -1,
		"garbage":                         -1,
	} {
		got, ok := ParseDate(in)
		if want < 0 && ok || want >= 0 && (!ok || got != want) {
			t.Errorf("ParseDate(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
}

func TestCodecNames(t *testing.T) {
	for name, want := range map[string]string{
		"UTF 8":        "\ufffda\ufffd",
		"koi8-ér":      "─aИ",
		"latin1é":      "\u0080aé",
		"utf８":         "\ufffda\ufffd",
		"cp1252":       "€aé",
		"windows_1252": "€aé",
		"us-ascii":     "\ufffda\ufffd",
		"uétf8":        "",
		"x.y":          "",
		"base64":       "",
		"aliases":      "",
	} {
		got, err := decodeBytes([]byte("\x80a\xe9"), name, errReplace)
		if want == "" && err == nil || want != "" && (err != nil || string(got) != want) {
			t.Errorf("decode %q = %q, %v; want %q", name, string(got), err, want)
		}
	}
}

func TestTransferDecoding(t *testing.T) {
	for in, want := range map[string]string{
		"YWJj":         "616263",
		"YWJ":          "6162",
		"YW=Jj":        "616263",
		"Y":            "59",
		"YWJjZA=":      "61626364",
		"YW\nJj":       "616263",
		"=YWJj":        "616263",
		"YWJjZA==ZQ==": "616263640650",
	} {
		if got := hex.EncodeToString(decodeB([]byte(in))); got != want {
			t.Errorf("decode_b(%q) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"a=3Db=\r\nc": "613d6263",
		"=ZZ=":        "3d5a5a",
		"x=\rmore\ny": "7879",
		"==41":        "3d3431",
	} {
		if got := hex.EncodeToString(a2bQP([]byte(in), false)); got != want {
			t.Errorf("a2b_qp(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestFilenameRFC2231(t *testing.T) {
	msg, err := Parse([]byte("Content-Type: text/plain; name*0*=utf-8''a%C3%A9; name*1=b\r\nContent-Disposition: attachment\r\n\r\nx"))
	if err != nil {
		t.Fatal(err)
	}
	name, ok, err := msg.Filename()
	att, aerr := msg.IsAttachment()
	if name != "aéb" || !ok || err != nil || !att || aerr != nil {
		t.Errorf("Filename = %q, %v, %v; IsAttachment = %v, %v", name, ok, err, att, aerr)
	}
}

func TestParseRaisesLikePython(t *testing.T) {
	raw := "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: application/pdf; name*\r=x\r\n\r\nbody\r\n--b--\r\n"
	if _, err := Parse([]byte(raw)); err == nil {
		t.Error("Parse accepted a part whose Content-Type Python cannot parse")
	}
}

func TestRelatedWithTextPayloadRaises(t *testing.T) {
	raw := "Content-Type: multipart/alternative; boundary=x\r\n\r\n--x\r\nContent-Type: multipart/related; boundary=x\r\n\r\npreamble\r\n--x--\r\n"
	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := msg.Body("plain"); !errors.Is(err, errNotMessage) {
		t.Errorf("Body error = %v, want errNotMessage", err)
	}
}

func nestedMultipart(depth int, leafType string) []byte {
	var b bytes.Buffer
	for i := range depth {
		fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=b%d\r\n\r\n--b%d\r\n", i, i)
	}
	b.WriteString("Content-Type: " + leafType + "\r\n\r\ndeep $5.00\r\n")
	return b.Bytes()
}

func nest(n int) string { return strings.Repeat("(", n) + "x" + strings.Repeat(")", n) }

func TestMIMEDepthLimit(t *testing.T) {
	for depth, ok := range map[int]bool{MaxDepth: true, MaxDepth + 1: false} {
		if _, err := Parse(nestedMultipart(depth, "text/plain")); (err == nil) != ok {
			t.Errorf("depth %d: err = %v, want ok %v", depth, err, ok)
		}
	}
}

func TestCommentLimits(t *testing.T) {
	for _, c := range []struct {
		name, value string
		level, max  int
		all         bool
	}{
		{"Content-Type", "text/plain; charset=utf-8 %s", 0, 484, false},
		{"Content-Type", "text/plain; charset=utf-8 %s", 10, 479, false},
		{"Content-Type", "text/plain; charset=utf-8 %s", 100, 434, false},
		{"From", "N %s <a@b.test>", 0, 487, false},
		{"To", "<a@z.test> %s", 0, 488, true},
		{"Message-ID", "<a@z.test> %s", 0, 491, false},
		{"Content-Disposition", "inline %s", 0, 489, false},
		{"Content-Disposition", "attachment; filename=a.pdf %s", 1, 486, false},
	} {
		for n, ok := range map[int]bool{c.max: true, c.max + 1: false} {
			v := escapeBytes([]byte(fmt.Sprintf(c.value, nest(n))))
			if _, err := headerValue([]rune(c.name), v, c.level, c.all); (err == nil) != ok {
				t.Errorf("%s level %d depth %d: err = %v, want ok %v", c.name, c.level, n, err, ok)
			}
		}
	}
	for n, ok := range map[int]bool{addrCommentLimit: true, addrCommentLimit + 1: false} {
		if _, err := GetAddresses([]string{"<a@z.test> " + nest(n)}); (err == nil) != ok {
			t.Errorf("getaddresses depth %d: err = %v, want ok %v", n, err, ok)
		}
	}
}

func TestNulCharsetRaises(t *testing.T) {
	if _, err := Parse([]byte("Content-Type: text/plain; charset*=utf-\x008''utf-8\r\n\r\nx")); err == nil {
		t.Error("Parse accepted a NUL in an RFC 2231 charset")
	}
	msg, err := Parse([]byte("Content-Disposition: inline; filename*=utf-8\x00''a.txt\r\n\r\nbody"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := msg.Body("plain"); err == nil {
		t.Error("Body accepted a NUL in an RFC 2231 charset")
	}
}
