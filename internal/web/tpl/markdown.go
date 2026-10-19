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

	"github.com/jeiang/atrium/internal/py"
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

// Markdown renders untrusted markdown to sanitised HTML: the Python markdown_html minus the Notion-flavoured
// steps. Raw HTML and indented code blocks are not parsed, so they render as escaped text like markdown-it.
func Markdown(text string) template.HTML {
	var b bytes.Buffer
	if err := renderer.Convert([]byte(mediaLinks(text)), &b); err != nil {
		return ""
	}
	cleaned := policy.Sanitize(b.String())
	cleaned, _ = emptyHeadRe.Sub("", cleaned, 0)
	cleaned, _ = taskRe.SubFunc(cleaned, func(m *rx.Match) string {
		checked := ""
		if m.Group(2) != " " {
			checked = " checked"
		}
		return m.Group(1) + `<input class="input" type="checkbox" disabled` + checked + "> "
	}, 0)
	return template.HTML(cleaned)
}

var (
	codeRe      = rx.MustCompile("(^[ \\t]*```[^\\n]*\\n.*?^[ \\t]*```[ \\t]*$|`[^`\\n]+`)", rx.DotAll|rx.Multiline)
	mediaRe     = rx.MustCompile(`(?<!\\)<(audio|video|file|pdf|embed)\b([^>]*?)(?:/>|>(.*?)</\1>)`, rx.DotAll)
	imageRe     = rx.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`, 0)
	attrRe      = rx.MustCompile(`([\w-]+)="([^"]*)"`, 0)
	emptyHeadRe = rx.MustCompile(`<thead>\s*<tr>\s*(?:<th>\s*</th>\s*)+</tr>\s*</thead>\s*`, 0)
	taskRe      = rx.MustCompile(`(<li>(?:\s*<p>)?)\[([ xX])\] `, 0)
)

func mediaLink(caption, url string) string {
	label := py.Strip(caption)
	if label == "" {
		label = url
	}
	if url == "" {
		return label
	}
	return "[" + label + "](<" + url + ">)"
}

func mediaLinks(text string) string {
	pieces, err := codeRe.Split(text, 0)
	if err != nil {
		return text
	}
	var b strings.Builder
	for i, piece := range pieces {
		if i%2 == 0 {
			piece, _ = mediaRe.SubFunc(piece, func(m *rx.Match) string {
				src := ""
				groups, _ := attrRe.FindAllGroups(m.Group(2))
				for _, g := range groups {
					if g[0] == "src" {
						src = g[1]
					}
				}
				return mediaLink(m.Group(3), src)
			}, 0)
			piece, _ = imageRe.SubFunc(piece, func(m *rx.Match) string { return mediaLink(m.Group(1), m.Group(2)) }, 0)
		}
		b.WriteString(piece)
	}
	return b.String()
}
