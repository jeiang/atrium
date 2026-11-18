package application

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant/webfetch"
	"github.com/jeiang/atrium/internal/textutil"
	"github.com/jeiang/atrium/internal/textx"
	"github.com/jeiang/atrium/internal/tool"
)

type WebFetchInput struct {
	URL         string
	MaxChars    *int64
	AllowedURLs []string
}

type WebPage struct {
	URL       string
	Title     string
	Text      string
	Truncated bool
}

func DescribeWebFetch() application.Definition {
	return application.Describe[WebFetchInput, WebPage](WebFetchName, application.Query, agentCallers, application.NoEffect, false)
}

func (s *Service) WebFetch(ctx context.Context, in WebFetchInput) (application.Read[WebPage], error) {
	url, err := webfetch.NormalizeURL(in.URL)
	if err != nil {
		return application.Read[WebPage]{}, err
	}
	lim, err := webfetch.ReadLimits(ctx, s.db.Read)
	if err != nil {
		return application.Read[WebPage]{}, err
	}
	maxChars := int64(lim.TextDefault)
	if in.MaxChars != nil {
		maxChars = max(1, min(*in.MaxChars, maxChars))
	}
	allowed := make(map[string]struct{}, len(in.AllowedURLs))
	for _, u := range in.AllowedURLs {
		n, err := webfetch.NormalizeURL(u)
		if err != nil {
			continue
		}
		allowed[n] = struct{}{}
	}
	fetcher := &webfetch.Fetcher{}
	if _, ok := allowed[url]; !ok && !tool.Approved(ctx) {
		if err := fetcher.CheckPublic(ctx, url); err != nil {
			return application.Read[WebPage]{}, tool.Error("fetch failed: " + err.Error())
		}
		return application.Read[WebPage]{}, &tool.NeedsApproval{Change: tool.Change{Title: "Fetch a page", Subject: url,
			Rule: "this URL was not typed by the operator or returned by web_search in this thread"}}
	}
	page, err := fetcher.FetchPage(ctx, url, lim)
	if err != nil {
		var te tool.Error
		if errors.As(err, &te) {
			return application.Read[WebPage]{}, err
		}
		return application.Read[WebPage]{}, tool.Error("fetch failed: " + err.Error())
	}
	markup := textutil.Decode(page.Body, page.Charset)
	title, body := webfetch.ExtractText(markup, page.URL, lim.PageChars)
	return application.Read[WebPage]{Data: WebPage{URL: page.URL, Title: title, Text: textx.Clip(body, int(maxChars)),
		Truncated: int64(utf8.RuneCountInString(body)) > maxChars}}, nil
}
