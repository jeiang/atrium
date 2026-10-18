package imapclient

import (
	"reflect"
	"testing"

	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

func TestPatterns(t *testing.T) {
	rxtest.Run(t, map[string]*rx.Regexp{
		"src/atrium/imap.py:LIST_LINE":                            listLineRE,
		"src/atrium/imap.py:TOKEN":                                tokenRE,
		"src/atrium/imap.py:parse_copyuid:ac698e2b":               copyUIDRE,
		"src/atrium/imap.py:_unquote:3d0d7e03":                    unquoteRE,
		"src/atrium/imap.py:parse_labels:a98611ec":                labelRE,
		"src/atrium/imap.py:_section:a403c216[FLAGS]":             flagsRE,
		"src/atrium/imap.py:_section:a403c216[X-GM-LABELS]":       labelsRE,
		"src/atrium/imap.py:structure_attachments:8d10bc65":       structureRE,
		"src/atrium/imap.py:parse_fetch:5f599446":                 groupStartRE,
		"src/atrium/imap.py:parse_fetch:94999554":                 sectionRE,
		"src/atrium/imap.py:parse_fetch:5f599446#2":               groupStartRE,
		"src/atrium/imap.py:parse_fetch:de5a295a":                 uidRE,
		"src/atrium/imap.py:_fetched:b7777f95":                    modseqRE,
		"src/atrium/imap.py:_fetched:41ca0e1d":                    sizeRE,
		"src/atrium/imap.py:_fetched:b296c151":                    gmMsgIDRE,
		"src/atrium/imap.py:_fetched:c4bf6001":                    gmThrIDRE,
		"src/atrium/imap.py:_fetched:34f2c716":                    internalRE,
		"src/atrium/imap.py:_fetched:de5a295a":                    uidRE,
		"src/atrium/imap.py:parse_status:b6ef0e5c[UIDVALIDITY]":   uidValidityRE,
		"src/atrium/imap.py:parse_status:b6ef0e5c[UIDNEXT]":       uidNextRE,
		"src/atrium/imap.py:parse_status:b6ef0e5c[HIGHESTMODSEQ]": highestRE,
		"src/atrium/imap.py:parse_status:b6ef0e5c[MESSAGES]":      messagesRE,
	})
}

func lines(texts ...string) []datum {
	out := make([]datum, len(texts))
	for i, s := range texts {
		out[i] = datum{text: []byte(s)}
	}
	return out
}

func TestParseListQuotedAndAtomNames(t *testing.T) {
	boxes, err := parseList(lines(
		`(\HasNoChildren \Trash) "/" "Deleted Messages"`,
		`(\HasNoChildren) "/" INBOX`,
		`(\HasChildren \Noselect) "/" "[Gmail]"`,
		`(\HasNoChildren) "/" "Caf&AOk-/Sub"`,
	))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, b := range boxes {
		names = append(names, b.Name)
	}
	if want := []string{"Deleted Messages", "INBOX", "[Gmail]", "Café/Sub"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %q, want %q", names, want)
	}
	if want := []string{`\HasNoChildren`, `\Trash`}; !reflect.DeepEqual(boxes[0].Flags, want) {
		t.Errorf("flags = %q", boxes[0].Flags)
	}
	if boxes[3].IMAPName != "Caf&AOk-/Sub" || boxes[0].Delimiter != "/" {
		t.Errorf("imap name %q, delimiter %q", boxes[3].IMAPName, boxes[0].Delimiter)
	}
}

func TestParseListSkipsLiteralsAndKeepsNILDelimiter(t *testing.T) {
	data := []datum{
		{text: []byte(`(\HasNoChildren) "/" {5}`), lit: []byte("Odd\"x"), tuple: true},
		{text: []byte(``)},
		{none: true},
		{text: []byte(`(\Noselect) NIL Archive`)},
		{text: []byte(`() "\\" "Quoted \"name\""`)},
	}
	boxes, err := parseList(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []mail.MailboxInfo{
		{Name: "Archive", IMAPName: "Archive", Flags: []string{`\Noselect`}, Delimiter: ""},
		{Name: `Quoted "name"`, IMAPName: `Quoted "name"`, Flags: []string{}, Delimiter: `\`},
	}
	if !reflect.DeepEqual(boxes, want) {
		t.Fatalf("got %#v", boxes)
	}
}

func TestParseLabelsMixedQuoting(t *testing.T) {
	got, err := parseLabels(`\Inbox "\\Important" "Personal/Friends" "Caf&AOk-"`)
	if want := []string{`\Inbox`, `\Important`, "Personal/Friends", "Café"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestParseStatus(t *testing.T) {
	st, err := parseStatus(lines("INBOX (MESSAGES 3 UIDNEXT 9 UIDVALIDITY 77 HIGHESTMODSEQ 1234)"))
	if err != nil || st.UIDValidity != 77 || st.UIDNext != 9 || st.Messages != 3 || st.HighestModSeq == nil || *st.HighestModSeq != 1234 {
		t.Fatalf("got %+v, %v", st, err)
	}
	st, err = parseStatus(lines("INBOX (MESSAGES 3 UIDNEXT 9 UIDVALIDITY 77)"))
	if err != nil || st.HighestModSeq != nil {
		t.Fatalf("highestmodseq: got %+v, %v", st, err)
	}
	if st, err = parseStatus([]datum{{none: true}}); err != nil || st != (mail.Status{}) {
		t.Fatalf("no STATUS response: got %+v, %v", st, err)
	}
}

func TestParseFetchFullGmailStyle(t *testing.T) {
	data := []datum{
		{
			text: []byte(`1 (X-GM-MSGID 111 X-GM-THRID 222 UID 5 RFC822.SIZE 900 INTERNALDATE "05-Oct-2026 10:11:12 +0000" ` +
				`BODYSTRUCTURE (("text" "plain" ("charset" "utf-8") NIL NIL "7bit" 10 1)` +
				`("application" "pdf" ("name" "Bill 1.pdf") NIL NIL "base64" 5000 NIL ("attachment" ("filename" "Bill 1.pdf")))` +
				` "mixed") BODY[HEADER] {9}`),
			lit:   []byte("Subject: x"),
			tuple: true,
		},
		{text: []byte(" BODY[TEXT]<0> {4}"), lit: []byte("body"), tuple: true},
		{text: []byte(` X-GM-LABELS (\Inbox "\\Important" "Personal/Friends") FLAGS (\Seen $Junk))`)},
	}
	items, err := parseFetch(data)
	if err != nil || len(items) != 1 {
		t.Fatalf("got %d items, %v", len(items), err)
	}
	want := mail.Message{
		UID: 5, Flags: []string{`\Seen`, "$Junk"}, Labels: []string{`\Inbox`, `\Important`, "Personal/Friends"},
		Size: 900, GmMsgID: "111", GmThrID: "222", InternalDate: "05-Oct-2026 10:11:12 +0000",
		Attachments: []mail.Attachment{{Kind: "application/pdf", Name: "Bill 1.pdf"}},
		Header:      []byte("Subject: x"), Text: []byte("body"),
	}
	if !reflect.DeepEqual(items[0], want) {
		t.Fatalf("got  %#v\nwant %#v", items[0], want)
	}
}

func TestParseFetchFlagOnlyLinesAndModseq(t *testing.T) {
	items, err := parseFetch(lines(`1 (UID 5 MODSEQ (900) FLAGS (\Seen \Flagged))`, `2 (UID 7 MODSEQ (901) FLAGS ())`))
	if err != nil || len(items) != 2 {
		t.Fatalf("got %d items, %v", len(items), err)
	}
	if items[0].UID != 5 || items[1].UID != 7 || items[0].ModSeq != 900 {
		t.Fatalf("got %+v", items)
	}
	if !reflect.DeepEqual(items[0].Flags, []string{`\Seen`, `\Flagged`}) || !reflect.DeepEqual(items[1].Flags, []string{}) {
		t.Fatalf("flags %q %q", items[0].Flags, items[1].Flags)
	}
	if items[1].Labels != nil || items[1].Attachments != nil || items[1].InternalDate != "" || items[1].Size != 0 {
		t.Fatalf("absent items must stay None: %#v", items[1])
	}
}

func TestParseFetchDropsGroupsWithoutUIDAndNone(t *testing.T) {
	items, err := parseFetch([]datum{{none: true}, {text: []byte(`3 (FLAGS (\Seen))`)}, {text: []byte(`4 (UID 9 FLAGS ())`)}})
	if err != nil || len(items) != 1 || items[0].UID != 9 {
		t.Fatalf("got %+v, %v", items, err)
	}
}

func TestStructureAttachments(t *testing.T) {
	tests := []struct {
		name, meta string
		want       []mail.Attachment
	}{
		{"none", `UID 1 FLAGS ()`, nil},
		{"not a list", `BODYSTRUCTURE NIL`, nil},
		{"unbalanced", `BODYSTRUCTURE ("text" "plain"`, nil},
		{"too short", `BODYSTRUCTURE ("text")`, []mail.Attachment{}},
		{"plain text", `BODYSTRUCTURE ("TEXT" "PLAIN" ("CHARSET" "us-ascii") NIL NIL "7BIT" 3 1 NIL NIL NIL)`, []mail.Attachment{}},
		{"inline name", `BODYSTRUCTURE (("text" "plain" NIL NIL NIL "7bit" 1 1)("image" "png" ("NAME" "a.png") NIL NIL "base64" 9 NIL NIL NIL) "related")`,
			[]mail.Attachment{{Kind: "image/png", Name: "a.png"}}},
		{"rfc822 without disposition", `BODYSTRUCTURE (("text" "plain" NIL NIL NIL "7bit" 1 1)("message" "rfc822" NIL NIL NIL "7bit" 9 (NIL "sub" NIL NIL NIL NIL NIL NIL NIL NIL) ("text" "plain" ("name" "x.txt") NIL NIL "7bit" 1 1) 3) "mixed")`,
			[]mail.Attachment{}},
		{"rfc822 attachment", `BODYSTRUCTURE (("message" "rfc822" NIL NIL NIL "7bit" 9 NIL NIL 3 NIL ("ATTACHMENT" ("filename" "fwd.eml"))) "mixed")`,
			[]mail.Attachment{{Kind: "message/rfc822", Name: "fwd.eml"}}},
		{"escaped quote", `BODYSTRUCTURE ("application" "pdf" ("name" "a \"b\".pdf") NIL NIL "base64" 9)`,
			[]mail.Attachment{{Kind: "application/pdf", Name: `a "b".pdf`}}},
	}
	for _, tt := range tests {
		got, err := structureAttachments(tt.meta)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: got %#v, %v; want %#v", tt.name, got, err, tt.want)
		}
	}
}

func TestFetchItemsRequestShape(t *testing.T) {
	labels := fetchOptions(mail.FetchItems{Labels: true})
	if !labels.Gmail.Labels || labels.BodySection != nil || labels.Gmail.MsgID {
		t.Errorf("fetch_items(True, False): %+v", labels)
	}
	body := fetchOptions(mail.FetchItems{Body: true})
	if body.Gmail.Labels || body.Gmail.MsgID || len(body.BodySection) != 2 || !body.BodySection[0].Peek || !body.BodySection[1].Peek {
		t.Errorf("fetch_items(False, True): %+v", body)
	}
	if p := body.BodySection[1].Partial; p == nil || p.Offset != 0 || p.Size != 65536 {
		t.Errorf("partial text: %+v", p)
	}
	full := fetchOptions(mail.FetchItems{Labels: true, Header: true, ChangedSince: 6})
	if !full.Gmail.MsgID || !full.Gmail.ThrID || !full.InternalDate || !full.RFC822Size || full.BodyStructure == nil || !full.BodyStructure.Extended || full.ChangedSince != 6 {
		t.Errorf("fetch_items(True, True): %+v", full)
	}
}

func TestParseCopyUID(t *testing.T) {
	got, err := parseCopyUID(lines("[COPYUID 38505 304,319:320 3956:3958] Done"))
	want := &mail.CopyUID{UIDValidity: 38505, Mapping: map[uint32]uint32{304: 3956, 319: 3957, 320: 3958}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got, err := parseCopyUID(lines("Done")); got != nil || err != nil {
		t.Fatalf("no COPYUID: got %+v, %v", got, err)
	}
	got, err = parseCopyUID([]datum{{none: true}, {text: []byte("COPYUID 7 5:3 10,11")}})
	if want := map[uint32]uint32{3: 10, 4: 11}; err != nil || !reflect.DeepEqual(got.Mapping, want) {
		t.Fatalf("descending range, short destination: got %+v, %v", got, err)
	}
	if _, err := parseCopyUID(lines("COPYUID 7 1:2:3 4")); err == nil {
		t.Fatal("1:2:3 must fail as int unpacking does")
	}
}

func TestParseSearch(t *testing.T) {
	if got, err := parseSearch(lines("101 103")); err != nil || !reflect.DeepEqual(got, []uint32{101, 103}) {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, d := range [][]datum{{{none: true}}, lines(""), lines("", "5")} {
		if got, err := parseSearch(d); err != nil || len(got) != 0 {
			t.Fatalf("empty search %v: got %v, %v", d, got, err)
		}
	}
}

func TestMapCRLF(t *testing.T) {
	if got := string(mapCRLF([]byte("a\nb\rc\r\nd\n\r"))); got != "a\r\nb\r\nc\r\nd\r\n\r\n" {
		t.Fatalf("got %q", got)
	}
}

func TestMailboxArg(t *testing.T) {
	for in, want := range map[string]string{"INBOX": "INBOX", "Caf&AOk-/Sub": "Café/Sub", "a&-b": "a&b", "": ""} {
		if got, err := mailboxArg(in); err != nil || got != want {
			t.Errorf("mailboxArg(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"a&b", "Café", "a\r\nb", "&A-"} {
		if got, err := mailboxArg(in); err == nil {
			t.Errorf("mailboxArg(%q) = %q, want an error", in, got)
		}
	}
}
