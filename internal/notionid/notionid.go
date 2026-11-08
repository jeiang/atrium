package notionid

import (
	"strings"

	"regexp"

	"github.com/jeiang/atrium/internal/py"
)

var (
	uuidHex = regexp.MustCompile(`[0-9a-fA-F]{32}`)
	dashed  = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	idRe    = regexp.MustCompile(`[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}`)
)

func Normalize(value string) string {
	text := py.Strip(value)
	if loc := dashed.FindStringIndex(text); loc != nil && loc[0] == 0 && loc[1] == len(text) {
		return strings.ToLower(text)
	}
	compact := strings.ReplaceAll(text, "-", "")
	if loc := uuidHex.FindStringIndex(compact); loc != nil && loc[0] == 0 && loc[1] == len(compact) {
		h := strings.ToLower(compact)
		return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
	}
	return text
}

func IDsInText(text string) []string {
	var found []string
	for _, id := range idRe.FindAllString(text, -1) {
		id = Normalize(id)
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
