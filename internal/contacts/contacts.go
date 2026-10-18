package contacts

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/mail/mime"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

const (
	Birthday    = "birthday"
	Anniversary = "anniversary"
)

var (
	unfoldRe   = rx.MustCompile(`\r?\n[ \t]`, 0)
	escapeRe   = rx.MustCompile(`\\([nN,;\\])`, 0)
	splitRe    = rx.MustCompile(`(?<!\\);`, 0)
	dateRe     = rx.MustCompile(`^(?:(\d{4})-?|--)(\d{2})-?(\d{2})(?!\d)`, 0)
	appleLabel = rx.MustCompile(`^_\$!<(.*)>!\$_$`, 0)
)

// Apple Contacts stores a placeholder year when none was entered.
var unknownYears = map[int]bool{1604: true, 1900: true}

var dateKinds = map[string]string{"BDAY": Birthday, "ANNIVERSARY": Anniversary, "X-ANNIVERSARY": Anniversary, "X-ABDATE": ""}

type Date struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Month int    `json:"month"`
	Day   int    `json:"day"`
	Year  *int   `json:"year"`
}

type Card struct {
	Name   string   `json:"name"`
	Emails []string `json:"emails"`
	Phones []string `json:"phones"`
	Org    string   `json:"org"`
	Dates  []Date   `json:"dates"`
}

type rawDate struct {
	kind, group string
	date        Date
}

type card struct {
	fn, n, org     string
	emails, phones []string
	dates          []rawDate
	labels         map[string]string
}

// Unfold is flags.py unfold and contacts.py unfold: it joins folded vCard lines.
func Unfold(text string) string {
	out, _ := unfoldRe.Sub("", text, 0)
	return out
}

func unescape(value string) string {
	out, _ := escapeRe.SubFunc(value, func(m *rx.Match) string {
		if g := m.Group(1); g != "n" && g != "N" {
			return g
		}
		return "\n"
	}, 0)
	return py.Strip(out)
}

func split(value string) []string {
	parts, _ := splitRe.Split(value, 0)
	return parts
}

// ParseDate reads YYYYMMDD, YYYY-MM-DD, --MMDD and --MM-DD; year is nil when unknown.
func ParseDate(value string) (month, day int, year *int, ok bool) {
	m, _ := dateRe.Match(py.Strip(value))
	if m == nil {
		return 0, 0, nil, false
	}
	month, day = decimal(m.Group(2)), decimal(m.Group(3))
	if month < 1 || month > 12 || day < 1 || day > monthDays(month) {
		return 0, 0, nil, false
	}
	if g, found := m.GroupOK(1); found {
		if y := decimal(g); y != 0 && !unknownYears[y] {
			year = new(y)
		}
	}
	return month, day, year, true
}

func monthDays(month int) int {
	switch month {
	case 2:
		return 29
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

func dateKind(label string) string {
	low := Casefold(label)
	switch {
	case strings.Contains(low, "anniversary"):
		return Anniversary
	case strings.Contains(low, "birth"):
		return Birthday
	}
	if s := py.Strip(label); s != "" {
		return s
	}
	return "date"
}

func cleanLabel(text string) string {
	text = unescape(text)
	if m, _ := appleLabel.Match(text); m != nil {
		text = m.Group(1)
	}
	return py.Strip(text)
}

// Parse reads every BEGIN:VCARD … END:VCARD block in text.
func Parse(text string) []Card {
	cards := []Card{}
	var c *card
	for _, line := range py.SplitLines(Unfold(text)) {
		head, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		prop, _, _ := strings.Cut(head, ";")
		group, name := "", prop
		if i := strings.LastIndexByte(prop, '.'); i >= 0 {
			group, name = prop[:i], prop[i+1:]
		}
		name = strings.ToUpper(name)
		switch {
		case name == "BEGIN" && strings.ToUpper(py.Strip(value)) == "VCARD":
			c = &card{labels: map[string]string{}}
		case name == "END" && c != nil:
			cards = append(cards, c.finish())
			c = nil
		case c != nil:
			c.collect(name, value, mime.Lower(group))
		}
	}
	return cards
}

func (c *card) collect(name, value, group string) {
	switch name {
	case "FN":
		c.fn = unescape(value)
	case "N":
		parts := split(value)
		family, given := parts[0], ""
		if len(parts) > 1 {
			given = parts[1]
		}
		c.n = joinNonEmpty(" ", unescape(given), unescape(family))
	case "EMAIL":
		email := mime.Lower(strings.TrimPrefix(unescape(value), "mailto:"))
		if strings.Contains(email, "@") && !slices.Contains(c.emails, email) {
			c.emails = append(c.emails, email)
		}
	case "TEL":
		phone := strings.TrimPrefix(unescape(value), "tel:")
		if phone != "" && !slices.Contains(c.phones, phone) {
			c.phones = append(c.phones, phone)
		}
	case "ORG":
		parts := split(value)
		for i, p := range parts {
			parts[i] = unescape(p)
		}
		c.org = joinNonEmpty(", ", parts...)
	case "BDAY", "ANNIVERSARY", "X-ANNIVERSARY", "X-ABDATE":
		if month, day, year, ok := ParseDate(value); ok {
			c.dates = append(c.dates, rawDate{kind: dateKinds[name], group: group, date: Date{Month: month, Day: day, Year: year}})
		}
	case "X-ABLABEL":
		if group != "" {
			c.labels[group] = cleanLabel(value)
		}
	}
}

func (c *card) finish() Card {
	out := Card{Name: c.fn, Emails: []string{}, Phones: []string{}, Org: c.org, Dates: []Date{}}
	out.Emails = append(out.Emails, c.emails...)
	out.Phones = append(out.Phones, c.phones...)
	switch {
	case out.Name != "":
	case c.n != "":
		out.Name = c.n
	case len(c.emails) > 0:
		out.Name = c.emails[0]
	}
	for _, item := range c.dates {
		label := ""
		if item.group != "" {
			label = c.labels[item.group]
		}
		entry := item.date
		entry.Kind = item.kind
		if entry.Kind == "" {
			entry.Kind = dateKind(label)
		}
		entry.Label = label
		if label == "" {
			entry.Label = entry.Kind
		}
		if !slices.ContainsFunc(out.Dates, entry.equal) {
			out.Dates = append(out.Dates, entry)
		}
	}
	return out
}

func (d Date) equal(o Date) bool {
	if d.Kind != o.Kind || d.Label != o.Label || d.Month != o.Month || d.Day != o.Day {
		return false
	}
	if d.Year == nil || o.Year == nil {
		return d.Year == o.Year
	}
	return *d.Year == *o.Year
}

func joinNonEmpty(sep string, parts ...string) string {
	kept := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// Load reads every *.vcf under dir, recursively, and returns the named cards sorted by casefolded name. An
// empty dir or one that is not a directory yields no cards.
func Load(dir string) []Card {
	found := []Card{}
	if info, err := os.Stat(dir); dir == "" || err != nil || !info.IsDir() {
		return found
	}
	for _, path := range VCFPaths(dir) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, c := range Parse(ReadText(data)) {
			if c.Name != "" {
				found = append(found, c)
			}
		}
	}
	keys := make(map[string]string, len(found))
	for _, c := range found {
		keys[c.Name] = Casefold(c.Name)
	}
	slices.SortStableFunc(found, func(a, b Card) int { return strings.Compare(keys[a.Name], keys[b.Name]) })
	return found
}

// VCFPaths lists the paths under dir that Path.rglob("*.vcf") yields, directories included.
func VCFPaths(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if strings.HasSuffix(e.Name(), ".vcf") {
			out = append(out, path)
		}
		if e.IsDir() {
			out = append(out, VCFPaths(path)...)
		}
	}
	return out
}

// Search is Filter over Load(dir).
func Search(dir, query string, limit int) []Card {
	return Filter(Load(dir), query, limit)
}

// Filter keeps the cards whose name, org, emails and phones contain every whitespace-separated term of query,
// casefolded. A limit of 0 keeps every match.
func Filter(cards []Card, query string, limit int) []Card {
	terms := py.Split(Casefold(query))
	out := []Card{}
	for _, c := range cards {
		haystack := Casefold(strings.Join(slices.Concat([]string{c.Name, c.Org}, c.Emails, c.Phones), " "))
		if !slices.ContainsFunc(terms, func(t string) bool { return !strings.Contains(haystack, t) }) {
			out = append(out, c)
			if limit != 0 && len(out) >= limit {
				break
			}
		}
	}
	return out
}
