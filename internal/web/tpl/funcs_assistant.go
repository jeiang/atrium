package tpl

import (
	"fmt"
	"html"
	"html/template"
	"reflect"
	"regexp"
	"strings"
)

func init() {
	registerFuncs(map[string]any{"has": has, "ctxlabel": ctxlabel, "toolverb": toolverb, "reflinks": reflinks})
}

var refTokenRe = regexp.MustCompile(`(?:^|\s)@thread/(\d+)(?:/m(\d+))?\b`)

func reflinks(v any) template.HTML {
	text := str(v)
	var b strings.Builder
	last := 0
	for _, m := range refTokenRe.FindAllStringSubmatchIndex(text, -1) {
		at := m[0] + strings.IndexByte(text[m[0]:m[1]], '@')
		b.WriteString(html.EscapeString(text[last:at]))
		href := "/chat/" + text[m[2]:m[3]]
		if m[4] >= 0 {
			href += "#m" + text[m[4]:m[5]]
		}
		b.WriteString(`<a class="underline underline-offset-4" href="` + html.EscapeString(href) + `">` + html.EscapeString(text[at:m[1]]) + `</a>`)
		last = m[1]
	}
	b.WriteString(html.EscapeString(text[last:]))
	return template.HTML(b.String())
}

func toolverb(name string) string {
	words := strings.Fields(strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(name))
	if len(words) == 0 {
		return name
	}
	words[0] = strings.ToUpper(words[0][:1]) + words[0][1:]
	return strings.Join(words, " ")
}

func has(list, item any) bool {
	rv := reflect.ValueOf(list)
	if list == nil || rv.Kind() != reflect.Slice {
		return false
	}
	for i := range rv.Len() {
		if reflect.DeepEqual(rv.Index(i).Interface(), item) {
			return true
		}
	}
	return false
}

func ctxlabel(v any) (string, error) {
	f, err := toFloat(v)
	if err != nil {
		return "", err
	}
	if f >= 1_000_000 {
		return fmt.Sprintf("%dM", int64(f/1_000_000)), nil
	}
	return fmt.Sprintf("%dK", int64(f/1000)), nil
}
