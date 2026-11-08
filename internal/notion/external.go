package notion

import (
	"regexp"
	"strings"

	"github.com/jeiang/atrium/internal/py"
)

var mediaSrcRe = regexp.MustCompile(`(?is)<(?:img|image|audio|video|file|pdf|embed|iframe)\b[^>]*?\bsrc="([^"]*)"`)

func ExternalURLs(markdown string) ([]string, error) {
	var found []string
	for _, loc := range imageRe.FindAllStringSubmatchIndex(markdown, -1) {
		found = append(found, group(markdown, loc, 2))
	}
	for _, loc := range mediaSrcRe.FindAllStringSubmatchIndex(markdown, -1) {
		if loc[0] > 0 && markdown[loc[0]-1] == '\\' {
			continue
		}
		found = append(found, group(markdown, loc, 1))
	}
	for _, loc := range mediaRe.FindAllStringSubmatchIndex(markdown, -1) {
		if loc[0] > 0 && markdown[loc[0]-1] == '\\' {
			continue
		}
		found = append(found, attrs(group(markdown, loc, 2))["src"])
	}
	out := []string{}
	for _, u := range found {
		u = strings.Trim(u, "<>")
		if !wikiURLRe.MatchString(u) {
			continue
		}
		dup := false
		for _, o := range out {
			dup = dup || o == u
		}
		if !dup {
			out = append(out, u)
		}
	}
	return out, nil
}

func ParentRef(parent py.Dict) (kind, id string) { return parentRef(parent) }
