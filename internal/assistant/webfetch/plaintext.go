package webfetch

import (
	"strings"

	"golang.org/x/net/html"

	"github.com/jeiang/atrium/internal/py"
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

func PlainText(markup string) (title, text string) {
	var (
		titleText strings.Builder
		parts     strings.Builder
		skip      int
		inTitle   bool
	)
	start := func(tag string) {
		switch {
		case tag == "title":
			inTitle = true
		case skipTags[tag]:
			skip++
		case blockTags[tag]:
			parts.WriteByte('\n')
		}
	}
	end := func(tag string) {
		switch {
		case tag == "title":
			inTitle = false
		case skipTags[tag]:
			skip = max(skip-1, 0)
		case blockTags[tag]:
			parts.WriteByte('\n')
		}
	}
	z := html.NewTokenizer(strings.NewReader(markup))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		switch tt {
		case html.StartTagToken:
			tag, _ := z.TagName()
			start(string(tag))
		case html.SelfClosingTagToken:
			tag, _ := z.TagName()
			start(string(tag))
			end(string(tag))
		case html.EndTagToken:
			tag, _ := z.TagName()
			end(string(tag))
		case html.TextToken:
			switch {
			case inTitle:
				titleText.Write(z.Text())
			case skip == 0:
				parts.Write(z.Text())
			}
		}
	}
	var lines []string
	for line := range strings.SplitSeq(parts.String(), "\n") {
		if line = collapse(line); line != "" {
			lines = append(lines, line)
		}
	}
	return collapse(titleText.String()), strings.Join(lines, "\n")
}
