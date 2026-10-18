// Package shared holds the filters every prototype approach uses (PROTOTYPE #75, throwaway).
package shared

import (
	"bytes"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

var md = goldmark.New(
	goldmark.WithExtensions(extension.Table, extension.Strikethrough),
	goldmark.WithRendererOptions(html.WithHardWraps()), // markdown-it breaks:true; raw HTML stays off (goldmark default)
)

var policy = func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("a", "blockquote", "br", "code", "del", "em", "h1", "h2", "h3", "h4", "h5", "h6", "hr",
		"li", "ol", "p", "pre", "s", "strong", "table", "tbody", "td", "th", "thead", "tr", "ul")
	p.AllowAttrs("href", "title").OnElements("a")
	p.AllowAttrs("class").OnElements("code")
	p.AllowURLSchemes("http", "https", "notion")
	p.AllowRelativeURLs(false)
	p.RequireParseableURLs(true)
	p.RequireNoReferrerOnLinks(true) // rel="noreferrer"; nh3 emits "noopener noreferrer"
	p.AddTargetBlankToFullyQualifiedLinks(false)
	return p
}()

// Markdown renders untrusted markdown to sanitised HTML (the Jinja `markdown` filter).
// Callers decide how to mark the result as trusted (template.HTML / templ.Raw / gonja safe).
func Markdown(text string) string {
	var b bytes.Buffer
	if err := md.Convert([]byte(text), &b); err != nil {
		return ""
	}
	return strings.TrimSpace(policy.Sanitize(b.String()))
}

// FileSizeFormat mirrors Jinja's filesizeformat (decimal units).
func FileSizeFormat(n int64) string {
	if n == 1 {
		return "1 Byte"
	}
	if n < 1000 {
		return fmt.Sprintf("%d Bytes", n)
	}
	units := []string{"kB", "MB", "GB", "TB", "PB"}
	v := float64(n)
	for i, u := range units {
		v /= 1000
		if v < 1000 || i == len(units)-1 {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return ""
}

// URLEncode mirrors Jinja's urlencode for a single string (percent-encode, "/" kept safe).
func URLEncode(s string) string {
	out := url.QueryEscape(s)
	out = strings.ReplaceAll(out, "+", "%20")
	return strings.ReplaceAll(out, "%2F", "/")
}

// Truncate mirrors Jinja's truncate(length, killwords=False, end="...", leeway=5).
func Truncate(s string, length int) string {
	const end, leeway = "...", 5
	if utf8.RuneCountInString(s) <= length+leeway {
		return s
	}
	r := []rune(s)
	cut := string(r[:length-len(end)])
	if i := strings.LastIndex(cut, " "); i >= 0 {
		cut = cut[:i]
	}
	return cut + end
}

// TS mirrors the app's `ts` filter: epoch seconds (UTC) to "date", "datetime" or "iso".
func TS(epoch float64, style string) string {
	if epoch == 0 {
		return ""
	}
	t := time.Unix(int64(epoch), 0).UTC()
	switch style {
	case "iso":
		return t.Format("2006-01-02T15:04:05") + "+00:00"
	case "date":
		return t.Format("2006-01-02")
	default:
		return t.Format("2006-01-02 15:04")
	}
}

// Round mirrors Python round() (banker's) then int, as used by `| round | int`.
func Round(f float64) int { return int(math.RoundToEven(f)) }
