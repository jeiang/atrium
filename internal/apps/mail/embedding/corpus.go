package embedding

import (
	"context"

	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/textutil"
)

func Document(ctx context.Context, subject, name, addr, body string) string {
	if subject == "" {
		subject = "none"
	}
	from := addr
	if name != "" {
		from = name + " <" + addr + ">"
	}
	return "title: " + subject + " | text: From: " + from + "\n\n" + textutil.Clip(body, endpoint.From(ctx).EmbedBodyChars)
}

var Corpus = embed.NewCorpus(embed.Source[Message]{
	Name:    "message",
	Columns: "account TEXT PARTITION KEY, ",
	Insert:  "INSERT INTO %s (rowid, account, embedding) VALUES (?, ?, ?)",
	Pending: "m.embedded_hash IS NULL",
	Missing: func(table string) string { return "m.id NOT IN (SELECT rowid FROM " + table + ")" },
	Load:    Pending,
	Reload:  Load,
	Document: func(ctx context.Context, m Message) string {
		return Document(ctx, m.Subject, m.Name, m.Addr, m.Body)
	},
	Hash:      func(m Message) string { return m.EmbeddedHash },
	Mark:      Mark,
	ID:        func(m Message) int64 { return m.ID },
	Partition: func(m Message) string { return m.Account },
	Reset:     Reset,
	Clear:     ClearIn,
	Counts:    Counts,
})
