package notionid

import (
	"strings"

	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

var (
	uuidHex = rx.MustCompile(`[0-9a-fA-F]{32}`, 0)
	dashed  = rx.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`, 0)
	idRe    = rx.MustCompile(`[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}`, 0)
)

func Normalize(value string) string {
	text := py.Strip(value)
	if m, _ := dashed.FullMatch(text); m != nil {
		return strings.ToLower(text)
	}
	compact := strings.ReplaceAll(text, "-", "")
	if m, _ := uuidHex.FullMatch(compact); m != nil {
		h := strings.ToLower(compact)
		return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
	}
	return text
}

func IDsInText(text string) []string {
	var found []string
	matches, _ := idRe.FindIter(text)
	for _, m := range matches {
		id := Normalize(m.Text())
		seen := false
		for _, f := range found {
			if f == id {
				seen = true
				break
			}
		}
		if !seen {
			found = append(found, id)
		}
	}
	return found
}
