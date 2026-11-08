package tpl

import (
	"bytes"
	"fmt"
	"html"
	"html/template"
	"slices"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"

	"regexp"

	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/notionid"
	"github.com/jeiang/atrium/internal/urlx"
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
	goldmark.WithRendererOptions(goldmarkhtml.WithHardWraps()),
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
	cleaned = emptyHeadRe.ReplaceAllString(cleaned, "")
	cleaned = subMatches(taskRe, cleaned, taskBox)
	cleaned = subMatches(hrefRe, cleaned, func(s string, loc []int) string { return internalHref(s, loc, pages) })
	return template.HTML(cleaned)
}

func subMatches(re *regexp.Regexp, s string, fn func(string, []int) string) string {
	var b strings.Builder
	last := 0
	for _, loc := range re.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(s[last:loc[0]])
		b.WriteString(fn(s, loc))
		last = loc[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

var (
	emptyHeadRe = regexp.MustCompile(`<thead>\s*<tr>\s*(?:<th>\s*</th>\s*)+</tr>\s*</thead>\s*`)
	taskRe      = regexp.MustCompile(`(<li>(?:\s*<p>)?)\[([ xX])\] `)
	hrefRe      = regexp.MustCompile(`href="([^"]*)"`)
)

var notionHosts = []string{"notion.so", "www.notion.so", "app.notion.com"}

func taskBox(s string, loc []int) string {
	checked := ""
	if s[loc[4]:loc[5]] != " " {
		checked = " checked"
	}
	return s[loc[2]:loc[3]] + `<input class="input" type="checkbox" disabled` + checked + "> "
}

func internalHref(s string, loc []int, pages map[string]string) string {
	text := s[loc[0]:loc[1]]
	url := html.UnescapeString(s[loc[2]:loc[3]])
	parts, err := urlx.URLSplit(url)
	if err != nil {
		return text
	}
	if host, _ := parts.Hostname(); !slices.Contains(notionHosts, host) {
		return text
	}
	known := ""
	for _, id := range notionid.IDsInText(url) {
		if _, ok := pages[id]; ok {
			known = id
		}
	}
	if known == "" {
		return text
	}
	return `href="/notion/` + known + `"`
}
