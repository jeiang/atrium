package webfetch

import (
	"strings"

	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/pyhtml"
)

var (
	skipTags  = setOf("script", "style", "noscript", "svg", "nav", "header", "footer", "aside", "form", "template")
	blockTags = setOf("p", "div", "br", "li", "ul", "ol", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "section", "article", "pre", "table")
)

func setOf(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func collapse(s string) string { return strings.Join(py.Split(s), " ") }

// PlainText is plain_text: the title and the text of the page with the content of SkipTags dropped, a line break
// around each block tag and every line's whitespace collapsed; empty lines are dropped.
func PlainText(markup string) (title, text string) {
	var (
		titleText strings.Builder
		parts     strings.Builder
		skip      int
		inTitle   bool
	)
	p := &pyhtml.Parser{
		StartTag: func(tag string, _ []pyhtml.Attr) {
			switch {
			case tag == "title":
				inTitle = true
			case skipTags[tag]:
				skip++
			case blockTags[tag]:
				parts.WriteByte('\n')
			}
		},
		EndTag: func(tag string) {
			switch {
			case tag == "title":
				inTitle = false
			case skipTags[tag]:
				skip = max(skip-1, 0)
			case blockTags[tag]:
				parts.WriteByte('\n')
			}
		},
		Data: func(data string) {
			switch {
			case inTitle:
				titleText.WriteString(data)
			case skip == 0:
				parts.WriteString(data)
			}
		},
	}
	p.Feed(markup)
	p.Close()
	var lines []string
	for line := range strings.SplitSeq(parts.String(), "\n") {
		if line = collapse(line); line != "" {
			lines = append(lines, line)
		}
	}
	return collapse(titleText.String()), strings.Join(lines, "\n")
}
