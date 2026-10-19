// Package tpltest compares Go template renders with the Jinja goldens in testdata/golden/html by canonical form.
package tpltest

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

type frame struct {
	tag      string
	verbatim bool
}

// Canonical is the token form of the golden README: one token per doctype, tag and text node, comments dropped,
// references decoded, attribute values and text whitespace collapsed outside pre, textarea and whitespace-pre*.
func Canonical(doc string) []string {
	z := html.NewTokenizer(strings.NewReader(doc))
	var out []string
	var text strings.Builder
	var stack []frame
	verbatim := func() bool { return len(stack) > 0 && stack[len(stack)-1].verbatim }
	dropNewline := false
	flush := func() {
		raw := text.String()
		text.Reset()
		if verbatim() {
			if raw != "" {
				out = append(out, "text "+fmt.Sprintf("%q", maskIDs(raw)))
			}
			return
		}
		if collapsed := collapse(raw); collapsed != "" {
			out = append(out, "text "+fmt.Sprintf("%q", maskIDs(collapsed)))
		}
	}
	for {
		kind := z.Next()
		switch kind {
		case html.ErrorToken:
			flush()
			return out
		case html.CommentToken:
		case html.TextToken:
			raw := string(z.Text())
			if dropNewline {
				raw = strings.TrimPrefix(raw, "\n")
				dropNewline = false
			}
			text.WriteString(raw)
		case html.DoctypeToken:
			flush()
			out = append(out, "<!doctype "+strings.ToLower(string(z.Text()))+">")
		case html.StartTagToken, html.SelfClosingTagToken:
			flush()
			dropNewline = false
			name, attrs := tag(z)
			out = append(out, "<"+name+attrs+">")
			if kind == html.StartTagToken && !void[name] {
				inherit := verbatim() || name == "pre" || name == "textarea" || strings.Contains(classOf(attrs), "whitespace-pre")
				stack = append(stack, frame{name, inherit})
				dropNewline = name == "pre" || name == "textarea"
			}
		case html.EndTagToken:
			flush()
			dropNewline = false
			name, _ := z.TagName()
			tagName := string(name)
			out = append(out, "</"+tagName+">")
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].tag == tagName {
					stack = stack[:i]
					break
				}
			}
		}
	}
}

var void = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true, "input": true,
	"link": true, "meta": true, "source": true, "track": true, "wbr": true,
}

func tag(z *html.Tokenizer) (string, string) {
	name, more := z.TagName()
	var b strings.Builder
	for more {
		var key, val []byte
		key, val, more = z.TagAttr()
		b.WriteByte(' ')
		b.Write(key)
		if value := collapse(string(val)); value != "" {
			b.WriteString(`="` + maskIDs(value) + `"`)
		}
	}
	return string(name), b.String()
}

func classOf(attrs string) string {
	_, rest, found := strings.Cut(attrs, ` class="`)
	if !found {
		return ""
	}
	value, _, _ := strings.Cut(rest, `"`)
	return value
}

func collapse(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f'
	}), " ")
}

func maskIDs(s string) string {
	const prefix = "cond-"
	var b strings.Builder
	for len(s) > 0 {
		i := strings.Index(s, prefix)
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i+len(prefix)])
		s = s[i+len(prefix):]
		if len(s) >= 6 && allDigits(s[:6]) {
			b.WriteString("NNNNNN")
			s = s[6:]
		}
	}
	return b.String()
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
