package tpl

import (
	"bytes"
	"fmt"
	"html/template"
	"slices"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
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
	if err := renderer.Convert([]byte(text), &b); err != nil {
		return ""
	}
	return template.HTML(policy.Sanitize(b.String()))
}
