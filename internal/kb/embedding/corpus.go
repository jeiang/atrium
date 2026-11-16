package embedding

import (
	"context"

	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/textutil"
)

func Document(ctx context.Context, title, aliases, body string) string {
	if title == "" {
		title = "none"
	}
	return "title: " + title + " | text: " + aliases + "\n\n" + textutil.Clip(body, endpoint.From(ctx).EmbedBodyChars)
}

var Corpus = embed.NewCorpus(embed.Source[Record]{
	Name:       "kb",
	Insert:     "INSERT INTO %s (rowid, embedding) VALUES (?, ?)",
	Background: true,
	Compare:    true,
	Missing:    func(table string) string { return "id NOT IN (SELECT rowid FROM " + table + ")" },
	Load:       Pending,
	Reload:     Load,
	Document: func(ctx context.Context, k Record) string {
		return Document(ctx, k.Title, k.Aliases, k.Body)
	},
	Hash:   func(k Record) string { return k.EmbeddedHash },
	Mark:   Mark,
	ID:     func(k Record) int64 { return k.ID },
	Clear:  ClearIn,
	Counts: Counts,
})
