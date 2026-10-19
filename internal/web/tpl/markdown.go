package tpl

import (
	"bytes"
	"fmt"
	"html/template"
	"slices"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"

	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/notionid"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/pyhtml"
	"github.com/jeiang/atrium/internal/rx"
)

var markdownTags = []string{
	"a", "blockquote", "br", "code", "del", "em", "h1", "h2", "h3", "h4", "h5", "h6", "hr",
	"li", "ol", "p", "pre", "s", "strong", "table", "tbody", "td", "th", "thead", "tr", "ul",
}

var renderer = goldmark.New(
	goldmark.WithExtensions(extension.Table, extension.Strikethrough),
	goldmark.WithParser(parser.NewParser(
		parser.WithBlockParsers(without(parser.DefaultBlockParsers(), "*parser.codeBlockParser", "*parser.htmlBlockParser")...),
		parser.WithInlineParsers(without(parser.DefaultInlineParsers(), "*parser.rawHTMLParser")...),
		parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
	)),
	goldmark.WithRendererOptions(html.WithHardWraps()),
)

var policy = func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements(markdownTags...)
	p.AllowAttrs("href", "title").OnElements("a")
	p.AllowAttrs("class").OnElements("code")
	p.AllowURLSchemes("http", "https", "notion")
	p.AllowRelativeURLs(false)
	p.RequireParseableURLs(true)
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(false)
	return p
}()

type indentedParagraph struct{ parser.BlockParser }

func (indentedParagraph) CanAcceptIndentedLine() bool { return true }

func without(parsers []util.PrioritizedValue, drop ...string) []util.PrioritizedValue {
	var kept []util.PrioritizedValue
	for _, p := range parsers {
		switch name := fmt.Sprintf("%T", p.Value); {
		case slices.Contains(drop, name):
		case name == "*parser.paragraphParser":
			kept = append(kept, util.Prioritized(indentedParagraph{p.Value.(parser.BlockParser)}, p.Priority))
		default:
			kept = append(kept, p)
		}
	}
	return kept
}

func Markdown(text string) template.HTML { return MarkdownWith(text, nil) }

func MarkdownWith(text string, pages map[string]string) template.HTML {
	var b bytes.Buffer
	if err := renderer.Convert([]byte(notion.Commonmark(text, pages)), &b); err != nil {
		return ""
	}
	cleaned := strings.ReplaceAll(policy.Sanitize(b.String()), `rel="noreferrer"`, `rel="noopener noreferrer"`)
	cleaned, _ = emptyHeadRe.Sub("", cleaned, 0)
	cleaned, _ = taskRe.SubFunc(cleaned, taskBox, 0)
	cleaned, _ = hrefRe.SubFunc(cleaned, func(m *rx.Match) string { return internalHref(m, pages) }, 0)
	return template.HTML(cleaned)
}

var (
	emptyHeadRe = rx.MustCompile(`<thead>\s*<tr>\s*(?:<th>\s*</th>\s*)+</tr>\s*</thead>\s*`, 0)
	taskRe      = rx.MustCompile(`(<li>(?:\s*<p>)?)\[([ xX])\] `, 0)
	hrefRe      = rx.MustCompile(`href="([^"]*)"`, 0)
)

var notionHosts = []string{"notion.so", "www.notion.so", "app.notion.com"}

func taskBox(m *rx.Match) string {
	checked := ""
	if m.Group(2) != " " {
		checked = " checked"
	}
	return m.Group(1) + `<input class="input" type="checkbox" disabled` + checked + "> "
}

func internalHref(m *rx.Match, pages map[string]string) string {
	url := pyhtml.Unescape(m.Group(1))
	parts, err := py.URLSplit(url)
	if err != nil {
		return m.Text()
	}
	if host, _ := parts.Hostname(); !slices.Contains(notionHosts, host) {
		return m.Text()
	}
	known := ""
	for _, id := range notionid.IDsInText(url) {
		if _, ok := pages[id]; ok {
			known = id
		}
	}
	if known == "" {
		return m.Text()
	}
	return `href="/notion/` + known + `"`
}
