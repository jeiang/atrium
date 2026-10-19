package notion

import (
	"strings"

	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

var mediaSrcRe = rx.MustCompile(`(?<!\\)<(?:img|image|audio|video|file|pdf|embed|iframe)\b[^>]*?\bsrc=\"([^\"]*)\"`, rx.IgnoreCase|rx.DotAll)

// ExternalURLs is external_urls: the URLs a renderer would fetch on view, markdown images and media or embed tags.
func ExternalURLs(markdown string) ([]string, error) {
	var found []string
	images, err := imageRe.FindIter(markdown)
	if err != nil {
		return nil, err
	}
	for _, m := range images {
		found = append(found, m.Group(2))
	}
	media, err := mediaSrcRe.FindIter(markdown)
	if err != nil {
		return nil, err
	}
	for _, m := range media {
		found = append(found, m.Group(1))
	}
	tags, err := mediaRe.FindIter(markdown)
	if err != nil {
		return nil, err
	}
	for _, m := range tags {
		found = append(found, attrs(m.Group(2))["src"])
	}
	out := []string{}
	for _, u := range found {
		u = strings.Trim(u, "<>")
		m, err := wikiURLRe.Match(u)
		if err != nil {
			return nil, err
		}
		if m == nil {
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

// ParentRef is parent_ref.
func ParentRef(parent py.Dict) (kind, id string) { return parentRef(parent) }
