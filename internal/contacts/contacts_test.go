package contacts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

func TestPatterns(t *testing.T) {
	rxtest.Run(t, map[string]*rx.Regexp{
		"src/atrium/flags.py:18":    unfoldRe,
		"src/atrium/contacts.py:6":  escapeRe,
		"src/atrium/contacts.py:7":  splitRe,
		"src/atrium/contacts.py:8":  dateRe,
		"src/atrium/contacts.py:10": appleLabel,
	})
}

func names(cards []Card) []string {
	out := []string{}
	for _, c := range cards {
		out = append(out, c.Name)
	}
	return out
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

const uiBackendVCF = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Ada\r\n  Lovelace\r\nN:Lovelace;Ada;;;\r\n" +
	"EMAIL;TYPE=HOME:Ada@Example.test\r\nitem1.EMAIL;type=WORK:ada@work.test\r\nTEL;TYPE=CELL:+1 555 0100\r\n" +
	"ORG:Analytical\\, Inc;Engines\r\nNOTE:long\r\n line\r\nEND:VCARD\r\n" +
	"BEGIN:VCARD\nVERSION:4.0\nN:Babbage;Charles;;;\nEMAIL:mailto:cb@example.test\nTEL;VALUE=uri:tel:+1-555-0101\nEND:VCARD\n"

func TestVCardParsing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.vcf"), uiBackendVCF)
	writeFile(t, filepath.Join(dir, "bad.vcf"), "garbage")
	cards := Load(dir)
	if got := names(cards); !reflect.DeepEqual(got, []string{"Ada Lovelace", "Charles Babbage"}) {
		t.Fatalf("names = %q", got)
	}
	ada, charles := cards[0], cards[1]
	if !reflect.DeepEqual(ada.Emails, []string{"ada@example.test", "ada@work.test"}) {
		t.Errorf("ada emails = %q", ada.Emails)
	}
	if !reflect.DeepEqual(ada.Phones, []string{"+1 555 0100"}) || ada.Org != "Analytical, Inc, Engines" {
		t.Errorf("ada phones, org = %q, %q", ada.Phones, ada.Org)
	}
	if !reflect.DeepEqual(charles.Emails, []string{"cb@example.test"}) || !reflect.DeepEqual(charles.Phones, []string{"+1-555-0101"}) {
		t.Errorf("charles = %+v", charles)
	}
	for query, want := range map[string][]string{
		"work.test":    {"Ada Lovelace"},
		"charles 0101": {"Charles Babbage"},
		"":             {"Ada Lovelace", "Charles Babbage"},
	} {
		if got := names(Search(dir, query, 0)); !reflect.DeepEqual(got, want) {
			t.Errorf("Search(%q) = %q, want %q", query, got, want)
		}
	}
	if got := Search("", "x", 0); got == nil || len(got) != 0 {
		t.Errorf("Search without a directory = %#v", got)
	}
}

const toolVCF = `BEGIN:VCARD
VERSION:3.0
FN:Ada Lovelace
EMAIL:ada@example.test
TEL:+15550001
ORG:Analytical Engines
END:VCARD
BEGIN:VCARD
VERSION:3.0
FN:Grace Hopper
EMAIL:grace@navy.test
END:VCARD
`

func TestLookupMatchesPeopleOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "book.vcf"), toolVCF)
	want := []Card{{Name: "Ada Lovelace", Emails: []string{"ada@example.test"}, Phones: []string{"+15550001"}, Org: "Analytical Engines", Dates: []Date{}}}
	if got := Search(dir, "ada", 5); !reflect.DeepEqual(got, want) {
		t.Errorf("ada = %#v", got)
	}
	if got := Search(dir, "navy.test", 5); len(got) != 1 || got[0].Name != "Grace Hopper" {
		t.Errorf("navy.test = %#v", got)
	}
	if got := Search(dir, "e", 1); len(got) != 1 {
		t.Errorf("limit 1 = %#v", got)
	}
	if got := Search(dir, "e", 0); len(got) != 2 {
		t.Errorf("no limit = %#v", got)
	}
}

func TestMissingOrEmptyDirectory(t *testing.T) {
	empty := t.TempDir()
	file := filepath.Join(empty, "file.vcf")
	writeFile(t, file, toolVCF)
	for name, dir := range map[string]string{
		"unset":    "",
		"missing":  filepath.Join(empty, "missing"),
		"empty":    t.TempDir(),
		"file":     file,
		"no cards": filepath.Join(empty, "sub"),
	} {
		if name == "no cards" {
			writeFile(t, filepath.Join(dir, "x.vcf"), "")
		}
		got := Load(dir)
		if got == nil || len(got) != 0 {
			t.Errorf("%s: Load = %#v", name, got)
		}
		if got := Search(dir, "ada", 5); got == nil || len(got) != 0 {
			t.Errorf("%s: Search = %#v", name, got)
		}
	}
}

func TestSymlinkedDirectory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "real", "book.vcf"), toolVCF)
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(root, "real"), link); err != nil {
		t.Fatal(err)
	}
	if got := names(Load(link)); !reflect.DeepEqual(got, []string{"Ada Lovelace", "Grace Hopper"}) {
		t.Errorf("Load(symlink) = %q", got)
	}
}

func oneCard(t *testing.T, lines ...string) Card {
	t.Helper()
	text := "BEGIN:VCARD\nVERSION:3.0\nFN:Ada Lovelace\n"
	for _, l := range lines {
		text += l + "\n"
	}
	cards := Parse(text + "END:VCARD")
	if len(cards) != 1 {
		t.Fatalf("Parse = %#v", cards)
	}
	return cards[0]
}

func TestBdayForms(t *testing.T) {
	for line, want := range map[string]Date{
		"BDAY:1990-05-17":                 {Month: 5, Day: 17, Year: new(1990)},
		"BDAY:19900517":                   {Month: 5, Day: 17, Year: new(1990)},
		"BDAY:--0517":                     {Month: 5, Day: 17},
		"BDAY:--05-17":                    {Month: 5, Day: 17},
		"BDAY;VALUE=date:1604-05-17":      {Month: 5, Day: 17},
		"item1.BDAY:1990-05-17T00:00:00Z": {Month: 5, Day: 17, Year: new(1990)},
	} {
		want.Kind, want.Label = Birthday, Birthday
		if got := oneCard(t, line).Dates; !reflect.DeepEqual(got, []Date{want}) {
			t.Errorf("%s: dates = %+v", line, got)
		}
	}
}

func TestBdayGarbageIsIgnored(t *testing.T) {
	if got := oneCard(t, "BDAY:soon", "BDAY:1990-13-40").Dates; len(got) != 0 {
		t.Errorf("dates = %+v", got)
	}
}

func TestAnniversaryForms(t *testing.T) {
	if got := oneCard(t, "ANNIVERSARY:20100601").Dates[0].Kind; got != Anniversary {
		t.Errorf("ANNIVERSARY kind = %q", got)
	}
	want := Date{Kind: Anniversary, Label: Anniversary, Month: 6, Day: 1, Year: new(2010)}
	if got := oneCard(t, "X-ANNIVERSARY:2010-06-01").Dates[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("X-ANNIVERSARY = %+v", got)
	}
	ios := oneCard(t,
		"item1.X-ABDATE;type=pointer:2012-03-04",
		"item1.X-ABLabel:_$!<Anniversary>!$_",
		"item2.X-ABDATE:--0909",
		"item2.X-ABLabel:Dog adoption",
	).Dates
	wantIOS := []Date{
		{Kind: Anniversary, Label: "Anniversary", Month: 3, Day: 4, Year: new(2012)},
		{Kind: "Dog adoption", Label: "Dog adoption", Month: 9, Day: 9},
	}
	if !reflect.DeepEqual(ios, wantIOS) {
		t.Errorf("ios = %+v", ios)
	}
}

func TestLabelBeforeDateAndUnlabeledAppleDate(t *testing.T) {
	found := oneCard(t, "item3.X-ABLabel:_$!<Anniversary>!$_", "item3.X-ABDATE:2001-02-03", "item4.X-ABDATE:--1231").Dates
	var kinds []string
	for _, d := range found {
		kinds = append(kinds, d.Kind)
	}
	if !reflect.DeepEqual(kinds, []string{Anniversary, "date"}) {
		t.Errorf("kinds = %q", kinds)
	}
}

func TestGoldenState(t *testing.T) {
	cards := Load(filepath.Join("..", "..", "testdata", "golden", "state", "contacts"))
	want := []Card{
		{Name: "Friend", Emails: []string{"friend@people.example"}, Phones: []string{}, Dates: []Date{{Kind: Birthday, Label: Birthday, Month: 10, Day: 17, Year: new(1990)}}},
		{Name: "Friend Two", Emails: []string{"friend2@people.example"}, Phones: []string{}, Dates: []Date{}},
		{Name: "Mom", Emails: []string{"mom@family.example"}, Phones: []string{}, Dates: []Date{{Kind: Birthday, Label: Birthday, Month: 10, Day: 20}}},
		{Name: "Pal", Emails: []string{"pal@people.example"}, Phones: []string{}, Dates: []Date{}},
	}
	if !reflect.DeepEqual(cards, want) {
		t.Errorf("Load(golden) = %+v", cards)
	}
}

func TestPythonFixtures(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		Cards  json.RawMessage `json:"cards"`
		Search []struct {
			Query string   `json:"query"`
			Names []string `json:"names"`
		} `json:"search"`
	}
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	cards := Load(filepath.Join("testdata", "book"))
	got, err := json.Marshal(cards)
	if err != nil {
		t.Fatal(err)
	}
	var gotAny, wantAny any
	if err := json.Unmarshal(got, &gotAny); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(expected.Cards, &wantAny); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotAny, wantAny) {
		t.Errorf("Load(testdata/book) =\n%s\nwant\n%s", got, expected.Cards)
	}
	for _, s := range expected.Search {
		if got := names(Filter(cards, s.Query, 0)); !reflect.DeepEqual(got, s.Names) {
			t.Errorf("Filter(%q) = %q, want %q", s.Query, got, s.Names)
		}
	}
}

func TestParseDate(t *testing.T) {
	for _, tc := range []struct {
		in         string
		month, day int
		year       *int
		ok         bool
	}{
		{" 2000-02-29 ", 2, 29, new(2000), true},
		{"2001-02-29", 2, 29, new(2001), true},
		{"2001-02-30", 0, 0, nil, false},
		{"2001-04-31", 0, 0, nil, false},
		{"0000-03-04", 3, 4, nil, true},
		{"1900-03-04", 3, 4, nil, true},
		{"--0304", 3, 4, nil, true},
		{"--03045", 0, 0, nil, false},
		{"20010304T120000", 3, 4, new(2001), true},
		{"\u0661\u0669\u0669\u0660-\u0660\u0665-\u0661\u0667", 5, 17, new(1990), true},
		{"1990-00-10", 0, 0, nil, false},
		{"1990-1-10", 0, 0, nil, false},
	} {
		month, day, y, ok := ParseDate(tc.in)
		if month != tc.month || day != tc.day || ok != tc.ok || !reflect.DeepEqual(y, tc.year) {
			t.Errorf("ParseDate(%q) = %d, %d, %v, %v", tc.in, month, day, y, ok)
		}
	}
}

func TestPythonStrings(t *testing.T) {
	if got := Casefold("Straße ÄRZTE ﬁ İ"); got != "strasse ärzte fi i̇" {
		t.Errorf("Casefold = %q", got)
	}
	for in, want := range map[string][]string{
		"":              nil,
		"a\nb":          {"a", "b"},
		"a\r\nb\rc\n":   {"a", "b", "c"},
		"\n":            {""},
		"a\x1cb\u2028c": {"a", "b", "c"},
		"a\n\nb":        {"a", "", "b"},
	} {
		if got := splitLines(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitLines(%q) = %q", in, got)
		}
	}
	for in, want := range map[string]string{
		"a\r\nb\rc":                 "a\nb\nc",
		"\xff\xe2\x82a\xf0\x9f\x98": "\ufffd\ufffda\ufffd",
		"\xed\xa0\x80":              "\ufffd\ufffd\ufffd",
		"\xc0\xaf":                  "\ufffd\ufffd",
		"é😀":                        "é😀",
		"\ufeffa\ufeffb":            "a\ufeffb",
		"\ufeff\ufeffa":             "\ufeffa",
	} {
		if got := readText([]byte(in)); got != want {
			t.Errorf("readText(%q) = %q, want %q", in, got, want)
		}
	}
}
