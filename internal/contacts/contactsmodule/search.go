package contactsmodule

import (
	"context"
	"net/url"

	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/search"
)

func SearchSources() []search.Source {
	return []search.Source{{Type: search.Contact, Find: findContacts}}
}

func findContacts(ctx context.Context, env search.Env, q search.Query) (search.Page, error) {
	src, err := contacts.Resolve(ctx, env.DB.Read)
	if err != nil || src.State != contacts.Ready {
		return search.Page{}, err
	}
	found := contacts.Search(src.Dir, q.Text, q.Offset+q.Limit)
	var page search.Page
	for _, c := range found[min(q.Offset, len(found)):] {
		title := c.Name
		if title == "" && len(c.Emails) > 0 {
			title = c.Emails[0]
		}
		snippet, matches := search.Excerpt(title, q.Text)
		page.Hits = append(page.Hits, search.Hit{Type: search.Contact, Ref: c.UID, Title: title, Detail: c.Org, Snippet: snippet, Matches: matches,
			Path: "/contacts?q=" + url.QueryEscape(title)})
	}
	return page, nil
}
