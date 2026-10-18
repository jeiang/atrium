package features

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

func TestPatterns(t *testing.T) {
	rxtest.Run(t, map[string]*rx.Regexp{
		"src/atrium/features.py:CURRENCY":                  currencyRE,
		"src/atrium/features.py:OTP":                       otpRE,
		"src/atrium/features.py:VCODE":                     vcodeRE,
		"src/atrium/features.py:REPLY_PREFIX":              replyPrefixRE,
		"src/atrium/features.py:subject_template:406a98ac": amountRE,
		"src/atrium/features.py:subject_template:7e7d9d58": monthDayRE,
		"src/atrium/features.py:subject_template:cf4d1562": dayMonthRE,
		"src/atrium/features.py:subject_template:adeb272e": numericDateRE,
		"src/atrium/features.py:subject_template:b52642fe": numberWordRE,
		"src/atrium/features.py:subject_template:4c87e833": hashNumberRE,
		"src/atrium/features.py:subject_template:088c52e8": spaceRE,
		"src/atrium/features.py:strip_markup:789e1f9c":     scriptStyleRE,
		"src/atrium/features.py:strip_markup:8b65452d":     tagRE,
		"src/atrium/features.py:strip_markup:088c52e8":     spaceRE,
		"src/atrium/features.py:filename_pattern:03bf5b4f": digitsRE,
		"src/atrium/features.py:body_text:088c52e8":        spaceRE,
		"src/atrium/features.py:normalize_msgid:b50aef46":  msgidStripRE,
		"src/atrium/features.py:msgid_list:4262beb1":       msgidRE,
		"src/atrium/features.py:extract:a0ed98d1":          dkimDomainRE,
	})
}

type record struct {
	Fn          string          `json:"fn"`
	Case        string          `json:"case"`
	Input       []string        `json:"input"`
	Output      json.RawMessage `json:"output"`
	File        string          `json:"file"`
	HeaderLen   int             `json:"header_len"`
	Attachments [][2]string     `json:"attachments"`
	FallbackTS  float64         `json:"fallback_ts"`
	Digest      string          `json:"digest"`
}

func loadCorpus(t *testing.T) []record {
	t.Helper()
	f, err := os.Open(filepath.Join(rxtest.Dir(), "features.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []record
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<24)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func asJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCorpus(t *testing.T) {
	zone, err := time.LoadLocation("America/Port_of_Spain")
	if err != nil {
		t.Fatal(err)
	}
	local := time.Local
	time.Local = zone
	t.Cleanup(func() { time.Local = local })

	records := loadCorpus(t)
	pass := 0
	for i, r := range records {
		var want any
		if err := json.Unmarshal(r.Output, &want); err != nil {
			t.Fatal(err)
		}
		var got any
		switch r.Fn {
		case "subject_template":
			got = SubjectTemplate(r.Input[0])
		case "filename_pattern":
			got = FilenamePattern(r.Input[0])
		case "msgid_list":
			got = MsgIDList(r.Input[0])
		case "normalize_msgid":
			got = NormalizeMsgID(r.Input[0])
		case "domain_of":
			got = DomainOf(r.Input[0])
		case "extract":
			raw, err := os.ReadFile(filepath.Join(rxtest.Dir(), r.File))
			if err != nil {
				t.Fatal(err)
			}
			header, text := raw[:r.HeaderLen], raw[r.HeaderLen:]
			var atts []Attachment
			if r.Attachments != nil {
				atts = make([]Attachment, len(r.Attachments))
				for j, a := range r.Attachments {
					atts[j] = Attachment{Type: a[0], Name: a[1]}
				}
			}
			got = mustExtract(t, header, text, atts, r.FallbackTS)
			if digest := ContentDigest(header, text); digest != r.Digest {
				t.Errorf("record %d %s: digest %s, want %s", i, r.Case, digest, r.Digest)
				continue
			}
		default:
			t.Fatalf("record %d: unknown fn %q", i, r.Fn)
		}
		if g := asJSON(t, got); !reflect.DeepEqual(g, want) {
			t.Errorf("record %d %s %s %q:\n got %v\nwant %v", i, r.Fn, r.Case, r.Input, g, want)
			continue
		}
		pass++
	}
	t.Logf("%d/%d records match", pass, len(records))
}

func TestPythonVectors(t *testing.T) {
	for in, want := range map[string]string{
		"Re: FWD: Order #12345 shipped": "order <n> shipped",
		"Payment of $1,234.50 received": "payment of <amt> received",
		"Statement for March 5, 2026":   "statement for <date>",
		"Due 2026-03-05":                "due <date>",
		"Your code ab12cd is ready":     "your code <n> is ready",
	} {
		if got := SubjectTemplate(in); got != want {
			t.Errorf("SubjectTemplate(%q) = %q, want %q", in, got, want)
		}
	}

	header := []byte("From: Some One <Some.One@Mail.Example.test>\r\nTo: me@x.test\r\nSubject: Receipt $12.00\r\n" +
		"Message-ID: <abc@mx.example.test>\r\nList-Id: <list.example.test>\r\n" +
		"Date: Tue, 06 Oct 2026 10:00:00 +0000\r\nContent-Type: text/plain\r\n\r\n")
	f := mustExtract(t, header, []byte("Your receipt total $12.00. Code 123456"), nil, 0)
	switch {
	case f.FromAddr != "some.one@mail.example.test", f.FromDomain != "mail.example.test",
		f.MsgID != "abc@mx.example.test", f.MsgIDDomain != "mx.example.test", f.ListID != "<list.example.test>",
		!f.HasMoney || !f.KReceipt || !f.KOTP6, !reflect.DeepEqual(f.ToAddrs, []string{"me@x.test"}), f.DateTS <= 0:
		t.Errorf("basic fields: %+v", f)
	}

	if f := mustExtract(t, []byte("\xff\xfe not a header"), nil, nil, 0); f.FromAddr != "" {
		t.Errorf("garbage from_addr = %q", f.FromAddr)
	}

	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	local := time.Local
	time.Local = tokyo
	t.Cleanup(func() { time.Local = local })
	minusZero := []byte("From: a@x.test\r\nDate: Tue, 06 Oct 2026 10:00:00 -0000\r\n\r\n")
	if got := mustExtract(t, minusZero, nil, nil, 0).DateTS; got != 1_791_280_800 {
		t.Errorf("-0000 date_ts = %v", got)
	}
	for in, want := range map[string]float64{
		"06-Oct-2026 10:00:00 -0000":      1_791_280_800,
		"Tue, 06 Oct 2026 19:00:00 +0900": 1_791_280_800,
	} {
		if got, ok := EmailTS(in); !ok || got != want {
			t.Errorf("EmailTS(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
}

func mustExtract(t *testing.T, header, text []byte, atts []Attachment, fallback float64) Features {
	t.Helper()
	f, err := Extract(header, text, atts, fallback)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestNulEncodedWordStaysLiteral(t *testing.T) {
	f := mustExtract(t, []byte("From: =?utf-\x008?q?N?= <a@x.test>\r\n\r\n"), nil, nil, 0)
	if f.FromAddr != "a@x.test" || f.FromName != "=?utf-\x008?q?N?=" {
		t.Errorf("from = %q, %q", f.FromAddr, f.FromName)
	}
}
