package kb

import (
	"strings"
)

const (
	BlockParagraph = "paragraph"
	BlockItem      = "item"
	BlockNumbered  = "numbered"
	BlockQuote     = "quote"
	BlockCode      = "code"
)

type PageBlock struct {
	Kind string
	Text string
}

type PageSection struct {
	Heading string
	Level   int
	Blocks  []PageBlock
}

func heading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level >= len(line) || line[level] != ' ' {
		return 0, "", false
	}
	return level, strings.TrimSpace(strings.TrimRight(strings.TrimSpace(line[level:]), "#")), true
}

func listItem(line string) (string, string, bool) {
	for _, marker := range []string{"- ", "* ", "+ "} {
		if text, ok := strings.CutPrefix(line, marker); ok {
			return BlockItem, strings.TrimSpace(text), true
		}
	}
	digits := 0
	for digits < len(line) && line[digits] >= '0' && line[digits] <= '9' {
		digits++
	}
	if digits > 0 && digits+1 < len(line) && (line[digits] == '.' || line[digits] == ')') && line[digits+1] == ' ' {
		return BlockNumbered, strings.TrimSpace(line[digits+2:]), true
	}
	return "", "", false
}

func Sections(body string) []PageSection {
	out := []PageSection{}
	current := PageSection{Blocks: []PageBlock{}}
	var lines []string
	kind := ""
	flush := func() {
		if len(lines) > 0 {
			current.Blocks = append(current.Blocks, PageBlock{Kind: kind, Text: strings.Join(lines, "\n")})
		}
		lines, kind = nil, ""
	}
	start := func(k string) {
		if kind != k {
			flush()
		}
		kind = k
	}
	code := false
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		fence := strings.HasPrefix(trimmed, "```")
		switch {
		case code && fence:
			flush()
			code = false
		case code:
			lines = append(lines, line)
		case fence:
			start(BlockCode)
			code = true
		case trimmed == "":
			flush()
		default:
			if level, text, ok := heading(trimmed); ok {
				flush()
				if current.Heading != "" || len(current.Blocks) > 0 {
					out = append(out, current)
				}
				current = PageSection{Heading: text, Level: level, Blocks: []PageBlock{}}
				continue
			}
			if k, text, ok := listItem(trimmed); ok {
				flush()
				kind, lines = k, []string{text}
				flush()
				continue
			}
			if text, ok := strings.CutPrefix(trimmed, ">"); ok {
				start(BlockQuote)
				lines = append(lines, strings.TrimSpace(text))
				continue
			}
			start(BlockParagraph)
			lines = append(lines, trimmed)
		}
	}
	flush()
	if current.Heading != "" || len(current.Blocks) > 0 {
		out = append(out, current)
	}
	return out
}
