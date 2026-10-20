package webfetch

import (
	"net/url"
	"strings"

	"github.com/markusmobius/go-trafilatura"
	"golang.org/x/net/html"

	"github.com/jeiang/atrium/internal/py"
)

func ExtractText(markup, pageURL string) (title, text string) {
	title, fallback := PlainText(markup)
	if t, body, ok := readable(markup, pageURL); ok {
		if t != "" {
			title = t
		}
		text = body
	}
	if text == "" {
		text = fallback
	}
	return collapse(title), py.Clip(text, PageTextChars)
}

func readable(markup, pageURL string) (title, text string, ok bool) {
	defer func() {
		if recover() != nil {
			title, text, ok = "", "", false
		}
	}()
	config := trafilatura.DefaultConfig()
	config.MinExtractedSize = 0
	config.MinOutputSize = 0
	config.MinOutputCommentSize = 0
	opts := trafilatura.Options{
		Config:          config,
		ExcludeComments: true,
		ExcludeTables:   true,
		EnableFallback:  true,
		HtmlDateMode:    trafilatura.Disabled,
	}
	if u, err := url.Parse(pageURL); err == nil {
		opts.OriginalURL = u
	}
	result, err := trafilatura.Extract(strings.NewReader(markup), opts)
	if err != nil || result == nil {
		return "", "", false
	}
	return result.Metadata.Title, renderText(result.ContentNode), true
}

var textBlocks = setOf("p", "div", "h1", "h2", "h3", "h4", "h5", "h6", "ul", "ol", "li", "dl", "dt", "dd", "blockquote", "pre",
	"table", "tr", "br", "hr", "section", "article", "figure", "body", "head", "list", "item", "quote", "row", "cell", "td", "th")

type textWriter struct {
	lines   []string
	current strings.Builder
}

func (w *textWriter) flush() {
	if line := collapse(w.current.String()); line != "" {
		w.lines = append(w.lines, line)
	}
	w.current.Reset()
}

func (w *textWriter) walk(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		w.current.WriteString(n.Data)
		return
	case html.ElementNode:
	default:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			w.walk(c)
		}
		return
	}
	if n.Data == "pre" || n.Data == "code" && blockContainer(n.Parent) {
		w.flush()
		var raw strings.Builder
		collectText(n, &raw)
		for line := range strings.SplitSeq(strings.Trim(raw.String(), "\n"), "\n") {
			if strings.TrimSpace(line) != "" {
				w.lines = append(w.lines, strings.TrimRight(line, " \t\r"))
			}
		}
		return
	}
	block := textBlocks[n.Data]
	if block {
		w.flush()
	}
	if n.Data == "li" || n.Data == "item" {
		w.current.WriteString("- ")
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c)
	}
	if block {
		w.flush()
	}
}

func collectText(n *html.Node, b *strings.Builder) {
	if n.Type == html.TextNode {
		b.WriteString(n.Data)
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectText(c, b)
	}
}

func renderText(root *html.Node) string {
	if root == nil {
		return ""
	}
	var w textWriter
	w.walk(root)
	w.flush()
	return strings.Join(w.lines, "\n")
}

func blockContainer(n *html.Node) bool {
	if n == nil || n.Type != html.ElementNode {
		return true
	}
	switch n.Data {
	case "body", "code", "div", "section", "article", "blockquote":
		return true
	}
	return false
}
