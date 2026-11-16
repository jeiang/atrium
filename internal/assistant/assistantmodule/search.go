package assistantmodule

import (
	"context"
	"slices"
	"strconv"
	"strings"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/search"
	"github.com/jeiang/atrium/internal/tasks"
	"github.com/jeiang/atrium/internal/textsearch"
	"github.com/jeiang/atrium/internal/textutil"
)

func SearchSources() []search.Source {
	return []search.Source{
		{Type: search.Thread, Find: findThreads},
		{Type: search.Record, Find: findRecords},
		{Type: search.Task, Find: findTasks},
		{Type: search.File, Find: findFiles},
		{Type: search.Space, Find: findSpaces},
	}
}

func findThreads(ctx context.Context, env search.Env, q search.Query) (search.Page, error) {
	match, err := textsearch.FTSQuery(q.Text)
	if err != nil || match == "" {
		return search.Page{}, err
	}
	found, err := threads.Find(ctx, env.DB.Read, match, q.Limit, q.Offset)
	if err != nil {
		return search.Page{}, err
	}
	var page search.Page
	for _, f := range found {
		snippet, matches := search.Marked(f.Snippet)
		h := search.Hit{Type: search.Thread, ID: f.ID, Snippet: snippet, Matches: matches, When: f.UpdatedAt, Path: "/threads/" + strconv.FormatInt(f.ID, 10)}
		if f.Title != nil {
			h.Title = *f.Title
		}
		if f.ProjectName != nil {
			h.Detail = *f.ProjectName
		}
		page.Hits = append(page.Hits, h)
	}
	return page, nil
}

func findRecords(ctx context.Context, env search.Env, q search.Query) (search.Page, error) {
	res, err := kb.Search(ctx, env.DB.Read, kb.SearchFilter{Query: q.Text, Limit: q.Offset + q.Limit, IncludePrivate: true, Now: q.Now,
		Embed: func(ctx context.Context, texts []string) ([][]float64, error) {
			url, err := embed.EmbedURL(ctx, env.DB.Read)
			if err != nil {
				return nil, err
			}
			ctx, err = embed.WithModels(ctx, env.DB.Read)
			if err != nil {
				return nil, err
			}
			return embed.Texts(ctx, url, texts)
		}})
	if err != nil {
		return search.Page{}, err
	}
	page := search.Page{Degraded: res.Skipped != ""}
	for _, h := range res.Hits[min(q.Offset, len(res.Hits)):] {
		snippet, matches := search.Marked(h.Snippet)
		if !strings.Contains(h.Snippet, textsearch.MarkStart) {
			snippet, matches = search.Excerpt(h.Body, q.Text)
		}
		page.Hits = append(page.Hits, search.Hit{Type: search.Record, ID: h.ID, Title: h.Title, Detail: h.Scope, Snippet: snippet, Matches: matches,
			When: h.UpdatedAt, Path: "/knowledge/" + strconv.FormatInt(h.ID, 10), Private: bool(h.Private)})
	}
	return page, nil
}

func findTasks(ctx context.Context, env search.Env, q search.Query) (search.Page, error) {
	found, err := tasks.Find(ctx, env.DB.Read, q.Text, q.Limit, q.Offset)
	if err != nil {
		return search.Page{}, err
	}
	var page search.Page
	for _, t := range found {
		snippet, matches := search.Excerpt(t.Title, q.Text)
		if len(matches) == 0 {
			snippet, matches = search.Excerpt(t.Notes, q.Text)
		}
		page.Hits = append(page.Hits, search.Hit{Type: search.Task, ID: t.ID, Title: t.Title, Detail: t.Status, Snippet: snippet, Matches: matches,
			When: t.UpdatedTS, Path: "/tasks/" + strconv.FormatInt(t.ID, 10)})
	}
	return page, nil
}

func findFiles(ctx context.Context, env search.Env, q search.Query) (search.Page, error) {
	found, err := asstapp.FindLibraryFiles(ctx, env.DB.Read, q.Text, q.Limit, q.Offset)
	if err != nil {
		return search.Page{}, err
	}
	var page search.Page
	for _, f := range found {
		snippet, matches := search.Excerpt(f.Name, q.Text)
		h := search.Hit{Type: search.File, ID: f.ID, Title: f.Name, Snippet: snippet, Matches: matches, When: f.CreatedAt}
		if f.ThreadID != 0 {
			h.Detail, h.Path = f.ThreadTitle, "/threads/"+strconv.FormatInt(f.ThreadID, 10)+"/files/"+strconv.FormatInt(f.ID, 10)
		} else {
			h.Detail, h.Path = f.ProjectName, "/knowledge/spaces/"+strconv.FormatInt(f.ProjectID, 10)+"/files/"+strconv.FormatInt(f.ID, 10)
		}
		page.Hits = append(page.Hits, h)
	}
	return page, nil
}

func findSpaces(ctx context.Context, env search.Env, q search.Query) (search.Page, error) {
	found, err := projects.List(ctx, env.DB.Read, false)
	if err != nil {
		return search.Page{}, err
	}
	terms := strings.Fields(textutil.Casefold(q.Text))
	var page search.Page
	skipped := 0
	for _, p := range found {
		haystack := textutil.Casefold(p.Name + " " + p.Instructions)
		if len(terms) == 0 || slices.ContainsFunc(terms, func(t string) bool { return !strings.Contains(haystack, t) }) {
			continue
		}
		if skipped < q.Offset {
			skipped++
			continue
		}
		snippet, matches := search.Excerpt(p.Name, q.Text)
		if len(matches) == 0 {
			snippet, matches = search.Excerpt(p.Instructions, q.Text)
		}
		page.Hits = append(page.Hits, search.Hit{Type: search.Space, ID: p.ID, Title: p.Name, Snippet: snippet, Matches: matches, When: p.UpdatedAt,
			Path: "/knowledge/spaces/" + strconv.FormatInt(p.ID, 10), Private: bool(p.Private)})
		if len(page.Hits) >= q.Limit {
			break
		}
	}
	return page, nil
}
