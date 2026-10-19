package notion

import (
	"strings"

	"github.com/jeiang/atrium/internal/notionid"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

const (
	markStart = "\x02"
	markEnd   = "\x03"
)

var (
	codeRe   = rx.MustCompile("(^[ \\t]*```[^\\n]*\\n.*?^[ \\t]*```[ \\t]*$|`[^`\\n]+`)", rx.DotAll|rx.Multiline)
	attrRe   = rx.MustCompile(`([\w-]+)="([^"]*)"`, 0)
	refRe    = rx.MustCompile(`(?<!\\)<(page|database|mention-page|mention-database|mention-data-source)\b([^>]*?)(?:/>|>(.*?)</\1>)`, rx.DotAll)
	mediaRe  = rx.MustCompile(`(?<!\\)<(audio|video|file|pdf|embed)\b([^>]*?)(?:/>|>(.*?)</\1>)`, rx.DotAll)
	dateRe   = rx.MustCompile(`(?<!\\)<mention-date\b([^>]*?)/>`, 0)
	tableRe  = rx.MustCompile(`(?<!\\)<table\b([^>]*)>(.*?)</table>`, rx.DotAll)
	rowRe    = rx.MustCompile(`<tr\b[^>]*>(.*?)</tr>`, rx.DotAll)
	cellRe   = rx.MustCompile(`<t[dh]\b[^>]*>(.*?)</t[dh]>`, rx.DotAll)
	emptyRe  = rx.MustCompile(`^[ \t]*<empty-block\s*/>[ \t]*$`, rx.Multiline)
	breakRe  = rx.MustCompile(`(?<!\\)<br\s*/?>`, 0)
	summary  = rx.MustCompile(`(?<!\\)<summary>(.*?)</summary>`, rx.DotAll)
	wrapper  = rx.MustCompile(`(?<!\\)</?(?:details|callout|columns|column|span|synced_block|synced_block_reference|mention-user|mention-agent|table_of_contents|custom-block|colgroup|col)\b[^>]*>`, 0)
	blockAtt = rx.MustCompile(`[ \t]*\{(?:[\w-]+="[^"]*"[ \t]*)+\}[ \t]*$`, rx.Multiline)
	imageRe  = rx.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`, 0)
	pipeRe   = rx.MustCompile(`(?<!\\)\|`, 0)

	linkRe     = rx.MustCompile(`\[([^\]]*)\]\([^)]*\)`, 0)
	headingRe  = rx.MustCompile(`^[ \t]*#{1,6}[ \t]+`, rx.Multiline)
	listMark   = rx.MustCompile(`^[ \t]*(?:[-*+]|\d+\.)[ \t]+(?:\[[ xX]\][ \t]+)?`, rx.Multiline)
	quoteRe    = rx.MustCompile(`^[ \t]*>[ \t]?`, rx.Multiline)
	ruleRe     = rx.MustCompile(`^[ \t]*(?:-{3,}|\*{3,})[ \t]*$`, rx.Multiline)
	emphasisRe = rx.MustCompile("\\*\\*|__|~~|`", 0)
	tagRe      = rx.MustCompile(`(?<!\\)</?[a-z][\w-]*\b[^<>]*>`, 0)
	cutTagRe   = rx.MustCompile(`(?<!\\)<[a-z/][^<>]*$`, 0)
	escapeRe   = rx.MustCompile(`\\([\\`+"`"+`*_{}\[\]()#+\-.!|~<>$^])`, 0)
)

func sub(re *rx.Regexp, s string, fn func(*rx.Match) string) string {
	out, err := re.SubFunc(s, fn, 0)
	if err != nil {
		return s
	}
	return out
}

func subText(re *rx.Regexp, repl, s string) string {
	out, err := re.Sub(repl, s, 0)
	if err != nil {
		return s
	}
	return out
}

func attrs(s string) map[string]string {
	out := map[string]string{}
	matches, _ := attrRe.FindIter(s)
	for _, m := range matches {
		out[m.Group(1)] = m.Group(2)
	}
	return out
}

func link(label, url string) string {
	if url == "" {
		return label
	}
	return "[" + label + "](<" + url + ">)"
}

func refLink(found *rx.Match, titles map[string]string) string {
	url := attrs(found.Group(2))["url"]
	title := py.Strip(found.Group(3))
	if title == "" {
		if ids := notionid.IDsInText(url); len(ids) > 0 {
			title = titles[ids[0]]
		}
		if title == "" {
			title = "Untitled"
		}
	}
	return link(title, url)
}

func mediaLink(caption, url string) string {
	label := py.Strip(caption)
	if label == "" {
		label = url
	}
	return link(label, url)
}

func dateText(found *rx.Match) string {
	a := attrs(found.Group(1))
	join := func(parts ...string) string {
		var kept []string
		for _, p := range parts {
			if p != "" {
				kept = append(kept, p)
			}
		}
		return strings.Join(kept, " ")
	}
	start := join(a["start"], a["startTime"])
	end := join(a["end"], a["endTime"])
	var kept []string
	for _, p := range []string{start, end} {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " → ")
}

func tableRows(found *rx.Match) ([][]string, int) {
	var rows [][]string
	rowMatches, _ := rowRe.FindIter(found.Group(2))
	for _, r := range rowMatches {
		var cells []string
		cellMatches, _ := cellRe.FindIter(r.Group(1))
		for _, c := range cellMatches {
			cells = append(cells, cellText(c.Group(1)))
		}
		rows = append(rows, cells)
	}
	width := 0
	for _, r := range rows {
		width = max(width, len(r))
	}
	for i, r := range rows {
		for len(r) < width {
			r = append(r, "")
		}
		rows[i] = r
	}
	return rows, width
}

func cellText(raw string) string {
	flat := subText(breakRe, " ", raw)
	return subText(pipeRe, `\\|`, strings.Join(py.Split(flat), " "))
}

func tableMarkdown(found *rx.Match) string {
	rows, width := tableRows(found)
	if len(rows) == 0 {
		return ""
	}
	head := make([]string, width)
	if attrs(found.Group(1))["header-row"] == "true" {
		head, rows = rows[0], rows[1:]
	}
	rule := make([]string, width)
	for i := range rule {
		rule[i] = "---"
	}
	var lines []string
	for _, r := range append([][]string{head, rule}, rows...) {
		lines = append(lines, "| "+strings.Join(r, " | ")+" |")
	}
	return "\n\n" + strings.Join(lines, "\n") + "\n\n"
}

func tableWords(found *rx.Match) string {
	rows, _ := tableRows(found)
	var cells []string
	for _, r := range rows {
		cells = append(cells, r...)
	}
	return "\n" + strings.Join(cells, " ") + "\n"
}

func convert(s string, titles map[string]string, table func(*rx.Match) string) string {
	part := func(chunk string) string {
		chunk = sub(tableRe, chunk, table)
		chunk = sub(refRe, chunk, func(m *rx.Match) string { return refLink(m, titles) })
		chunk = sub(mediaRe, chunk, func(m *rx.Match) string { return mediaLink(m.Group(3), attrs(m.Group(2))["src"]) })
		chunk = sub(imageRe, chunk, func(m *rx.Match) string { return mediaLink(m.Group(1), m.Group(2)) })
		chunk = sub(dateRe, chunk, dateText)
		chunk = sub(summary, chunk, func(m *rx.Match) string { return "**" + py.Strip(m.Group(1)) + "**" })
		chunk = subText(emptyRe, "", chunk)
		chunk = subText(wrapper, "", chunk)
		chunk = subText(breakRe, "\n", chunk)
		return subText(blockAtt, "", chunk)
	}
	pieces, err := codeRe.Split(s, 0)
	if err != nil {
		return s
	}
	var b strings.Builder
	for i, piece := range pieces {
		if i%2 == 1 {
			b.WriteString(piece)
		} else {
			b.WriteString(part(piece))
		}
	}
	return b.String()
}

// Commonmark is commonmark: Notion's enhanced markdown as plain CommonMark.
func Commonmark(s string, titles map[string]string) string {
	return convert(s, titles, tableMarkdown)
}

// Plain is plain: the readable words of Notion markdown, with the search highlight markers kept balanced.
func Plain(s string, titles map[string]string) string {
	readable := convert(s, titles, tableWords)
	for _, step := range []struct {
		re   *rx.Regexp
		repl string
	}{
		{linkRe, `\1`}, {headingRe, ""}, {listMark, ""}, {quoteRe, ""}, {ruleRe, ""},
		{emphasisRe, ""}, {tagRe, ""}, {cutTagRe, ""}, {escapeRe, `\1`},
	} {
		readable = subText(step.re, step.repl, readable)
	}
	return balanced(strings.Join(py.Split(readable), " "))
}

func balanced(s string) string {
	out := make([]rune, 0, len(s))
	openAt := -1
	for _, ch := range s {
		switch string(ch) {
		case markStart:
			if openAt >= 0 {
				continue
			}
			openAt = len(out)
		case markEnd:
			if openAt < 0 {
				continue
			}
			openAt = -1
		}
		out = append(out, ch)
	}
	if openAt >= 0 {
		out = append(out[:openAt], out[openAt+1:]...)
	}
	return string(out)
}
