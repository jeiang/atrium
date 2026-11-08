package notion

import (
	"regexp"
	"strings"

	"github.com/jeiang/atrium/internal/notionid"
)

const (
	markStart = "\x02"
	markEnd   = "\x03"
)

var (
	codeRe   = regexp.MustCompile("(?sm)(^[ \\t]*```[^\\n]*\\n.*?^[ \\t]*```[ \\t]*$|`[^`\\n]+`)")
	attrRe   = regexp.MustCompile(`([\w-]+)="([^"]*)"`)
	refRe    = regexp.MustCompile(`<(page|database|mention-page|mention-database|mention-data-source)\b([^>]*?)(?:/>|>(.*?)</(?:page|database|mention-page|mention-database|mention-data-source)>)`)
	mediaRe  = regexp.MustCompile(`<(audio|video|file|pdf|embed)\b([^>]*?)(?:/>|>(.*?)</(?:audio|video|file|pdf|embed)>)`)
	dateRe   = regexp.MustCompile(`<mention-date\b([^>]*?)/>`)
	tableRe  = regexp.MustCompile(`(?s)<table\b([^>]*)>(.*?)</table>`)
	rowRe    = regexp.MustCompile(`(?s)<tr\b[^>]*>(.*?)</tr>`)
	cellRe   = regexp.MustCompile(`(?s)<t[dh]\b[^>]*>(.*?)</t[dh]>`)
	emptyRe  = regexp.MustCompile(`(?m)^[ \t]*<empty-block\s*/>[ \t]*$`)
	breakRe  = regexp.MustCompile(`<br\s*/?>`)
	summary  = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)
	wrapper  = regexp.MustCompile(`</?(?:details|callout|columns|column|span|synced_block|synced_block_reference|mention-user|mention-agent|table_of_contents|custom-block|colgroup|col)\b[^>]*>`)
	blockAtt = regexp.MustCompile(`(?m)[ \t]*\{(?:[\w-]+="[^"]*"[ \t]*)+\}[ \t]*$`)
	imageRe  = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)
	pipeRe   = regexp.MustCompile(`\|`)

	linkRe     = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	headingRe  = regexp.MustCompile(`(?m)^[ \t]*#{1,6}[ \t]+`)
	listMark   = regexp.MustCompile(`(?m)^[ \t]*(?:[-*+]|\d+\.)[ \t]+(?:\[[ xX]\][ \t]+)?`)
	quoteRe    = regexp.MustCompile(`(?m)^[ \t]*>[ \t]?`)
	ruleRe     = regexp.MustCompile(`(?m)^[ \t]*(?:-{3,}|\*{3,})[ \t]*$`)
	emphasisRe = regexp.MustCompile("\\*\\*|__|~~|`")
	tagRe      = regexp.MustCompile(`</?[a-z][\w-]*\b[^<>]*>`)
	cutTagRe   = regexp.MustCompile(`<[a-z/][^<>]*$`)
	escapeRe   = regexp.MustCompile(`\\([\\` + "`" + `*_{}\[\]()#+\-.!|~<>$^])`)
)

func group(s string, loc []int, i int) string {
	if loc[2*i] < 0 {
		return ""
	}
	return s[loc[2*i]:loc[2*i+1]]
}

func sub(re *regexp.Regexp, s string, skipEscaped bool, fn func(loc []int) string) string {
	var b strings.Builder
	last := 0
	for _, loc := range re.FindAllStringSubmatchIndex(s, -1) {
		if skipEscaped && loc[0] > 0 && s[loc[0]-1] == '\\' {
			continue
		}
		b.WriteString(s[last:loc[0]])
		b.WriteString(fn(loc))
		last = loc[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

func subText(re *regexp.Regexp, repl, s string) string { return re.ReplaceAllString(s, repl) }

func attrs(s string) map[string]string {
	out := map[string]string{}
	for _, loc := range attrRe.FindAllStringSubmatchIndex(s, -1) {
		out[group(s, loc, 1)] = group(s, loc, 2)
	}
	return out
}

func link(label, url string) string {
	if url == "" {
		return label
	}
	return "[" + label + "](<" + url + ">)"
}

func refLink(s string, loc []int, titles map[string]string) string {
	url := attrs(group(s, loc, 2))["url"]
	title := strings.TrimSpace(group(s, loc, 3))
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
	label := strings.TrimSpace(caption)
	if label == "" {
		label = url
	}
	return link(label, url)
}

func dateText(s string, loc []int) string {
	a := attrs(group(s, loc, 1))
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

func tableRows(s string, loc []int) ([][]string, int) {
	var rows [][]string
	for _, r := range rowRe.FindAllStringSubmatchIndex(group(s, loc, 2), -1) {
		var cells []string
		inner := group(s, loc, 2)
		for _, c := range cellRe.FindAllStringSubmatchIndex(group(inner, r, 1), -1) {
			cells = append(cells, cellText(group(group(inner, r, 1), c, 1)))
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
	flat := sub(breakRe, raw, true, func([]int) string { return " " })
	return sub(pipeRe, flat, true, func([]int) string { return `\\|` })
}

func tableMarkdown(s string, loc []int) string {
	rows, width := tableRows(s, loc)
	if len(rows) == 0 {
		return ""
	}
	head := make([]string, width)
	if attrs(group(s, loc, 1))["header-row"] == "true" {
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

func tableWords(s string, loc []int) string {
	rows, _ := tableRows(s, loc)
	var cells []string
	for _, r := range rows {
		cells = append(cells, r...)
	}
	return "\n" + strings.Join(cells, " ") + "\n"
}

func convert(s string, titles map[string]string, table func(string, []int) string) string {
	part := func(chunk string) string {
		chunk = sub(tableRe, chunk, true, func(loc []int) string { return table(chunk, loc) })
		chunk = sub(refRe, chunk, true, func(loc []int) string { return refLink(chunk, loc, titles) })
		chunk = sub(mediaRe, chunk, true, func(loc []int) string {
			return mediaLink(group(chunk, loc, 3), attrs(group(chunk, loc, 2))["src"])
		})
		chunk = sub(imageRe, chunk, false, func(loc []int) string {
			return mediaLink(group(chunk, loc, 1), group(chunk, loc, 2))
		})
		chunk = sub(dateRe, chunk, true, func(loc []int) string { return dateText(chunk, loc) })
		chunk = sub(summary, chunk, true, func(loc []int) string { return "**" + strings.TrimSpace(group(chunk, loc, 1)) + "**" })
		chunk = subText(emptyRe, "", chunk)
		chunk = sub(wrapper, chunk, true, func([]int) string { return "" })
		chunk = sub(breakRe, chunk, true, func([]int) string { return "\n" })
		return subText(blockAtt, "", chunk)
	}
	pieces := splitKeep(codeRe, s)
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

func splitKeep(re *regexp.Regexp, s string) []string {
	var out []string
	last := 0
	for _, loc := range re.FindAllStringSubmatchIndex(s, -1) {
		out = append(out, s[last:loc[0]])
		for i := 1; i < len(loc)/2; i++ {
			out = append(out, group(s, loc, i))
		}
		last = loc[1]
	}
	return append(out, s[last:])
}

func Commonmark(s string, titles map[string]string) string {
	return convert(s, titles, tableMarkdown)
}

func Plain(s string, titles map[string]string) string {
	readable := convert(s, titles, tableWords)
	for _, step := range []struct {
		re      *regexp.Regexp
		repl    string
		escaped bool
	}{
		{linkRe, "${1}", false}, {headingRe, "", false}, {listMark, "", false}, {quoteRe, "", false},
		{ruleRe, "", false}, {emphasisRe, "", false}, {tagRe, "", true}, {cutTagRe, "", true},
		{escapeRe, "${1}", false},
	} {
		if step.escaped {
			readable = sub(step.re, readable, true, func([]int) string { return step.repl })
			continue
		}
		readable = subText(step.re, step.repl, readable)
	}
	return balanced(strings.Join(strings.Fields(readable), " "))
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
