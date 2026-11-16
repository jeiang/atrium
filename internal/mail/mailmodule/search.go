package mailmodule

import (
	"context"
	"strconv"

	mailsearch "github.com/jeiang/atrium/internal/mail/search"
	"github.com/jeiang/atrium/internal/search"
)

func SearchSources() []search.Source {
	return []search.Source{{Type: search.Message, Find: findMessages}}
}

func findMessages(ctx context.Context, env search.Env, q search.Query) (search.Page, error) {
	var accounts []string
	for _, a := range env.Config.Accounts() {
		if a.Enabled {
			accounts = append(accounts, a.Name)
		}
	}
	found, err := mailsearch.Find(ctx, env.DB.Read, q.Text, accounts, q.Limit, q.Offset)
	if err != nil {
		return search.Page{}, err
	}
	var page search.Page
	for _, m := range found {
		snippet, matches := search.Marked(m.Snippet)
		detail := m.FromName
		if detail == "" {
			detail = m.FromAddr
		}
		page.Hits = append(page.Hits, search.Hit{Type: search.Message, ID: m.ID, Title: m.Subject, Detail: detail, Snippet: snippet, Matches: matches,
			When: m.DateTS, Path: "/mail/messages/" + strconv.FormatInt(m.ID, 10)})
	}
	return page, nil
}
