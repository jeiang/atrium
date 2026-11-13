package application

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/message"
	"github.com/jeiang/atrium/internal/mail/search"
	"github.com/jeiang/atrium/internal/mail/triage"
	"github.com/jeiang/atrium/internal/timex"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	MessageName application.Name = "mail.message"
	SearchName  application.Name = "mail.search"
)

type MessageInput struct {
	ID       int64
	Tool     bool
	MaxChars int64
	Draft    bool
	Folders  bool
}
type MessageResult struct {
	View    *message.View
	Draft   *drafts.Row
	Folders []triage.Folder
	Read    *ReadMessage
	Marks   tool.Marks
}
type ReadMessage struct {
	Untrusted   bool           `json:"untrusted"`
	ID          int64          `json:"message_id"`
	Account     string         `json:"account"`
	Folder      string         `json:"folder"`
	Date        string         `json:"date"`
	Headers     map[string]any `json:"headers"`
	Body        string         `json:"body"`
	Truncated   bool           `json:"truncated"`
	Attachments []any          `json:"attachments"`
}
type SearchInput struct {
	Query   string
	Filters search.Filters
	Limit   int
	Embed   embed.TextsFunc `json:"-"`
}
type SearchHit struct {
	ID      int64  `json:"message_id"`
	Account string `json:"account"`
	Folder  string `json:"folder"`
	Date    string `json:"date"`
	From    string `json:"from"`
	To      any    `json:"to"`
	Subject string `json:"subject"`
	Snippet string `json:"snippet"`
}
type SearchResult struct {
	Semantic bool        `json:"semantic"`
	Results  []SearchHit `json:"results"`
	Marks    tool.Marks  `json:"-"`
}

func DescribeMessage() application.Definition {
	return application.Describe[MessageInput, MessageResult](MessageName, application.Query, []application.Authority{application.Operator, application.Agent}, application.NoEffect, false)
}
func DescribeSearch() application.Definition {
	return application.Describe[SearchInput, SearchResult](SearchName, application.Query, []application.Authority{application.Agent}, application.NoEffect, false)
}
func clip(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}

func iso(ts float64) string {
	if ts == 0 {
		return ""
	}
	sec, _ := timex.SplitTimestamp(ts)
	return time.Unix(sec, 0).UTC().Format("2006-01-02T15:04:05-07:00")
}

func recipients(ctx context.Context, q db.Querier, ids []int64) (map[int64]any, error) {
	raw, err := mail.Recipients(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]any, len(raw))
	for id, text := range raw {
		if text == "" {
			text = "[]"
		}
		var v any
		if err := json.Unmarshal([]byte(text), &v); err != nil {
			return nil, tool.Error("invalid recipients JSON")
		}
		out[id] = v
	}
	return out, nil
}

func (s *Service) Search(ctx context.Context, in SearchInput) (application.Read[SearchResult], error) {
	found, err := search.Search(ctx, s.db.KNN, search.Query{Text: in.Query, Filters: in.Filters, Limit: in.Limit}, in.Embed)
	if err != nil {
		return application.Read[SearchResult]{}, err
	}
	ids := make([]int64, len(found.Hits))
	for i, h := range found.Hits {
		ids[i] = h.ID
	}
	to, err := recipients(ctx, s.db.Read, ids)
	if err != nil {
		return application.Read[SearchResult]{}, err
	}
	results := make([]SearchHit, len(found.Hits))
	marks := tool.Marks(0)
	for i, h := range found.Hits {
		from := h.FromAddr
		if h.FromName != "" {
			from = h.FromName + " <" + h.FromAddr + ">"
		}
		recips, ok := to[h.ID]
		if !ok {
			recips = []any{}
		}
		if list, ok := recips.([]any); h.FromAddr != "" || (ok && len(list) > 0) {
			marks |= tool.PII
		}
		if h.Subject != "" || h.Snippet != "" {
			marks |= tool.Messages | tool.Untrusted
		}
		results[i] = SearchHit{h.ID, h.Account, h.Folder, iso(h.DateTS), from, recips, h.Subject, tool.Clip(h.Snippet, assistant.TuningAgentMailSnippetChars)}
	}
	rev, err := s.snapshot(ctx, SearchName, []any{in.Query, in.Filters, in.Limit}, func(db.Querier) error { return nil })
	return application.Read[SearchResult]{Revision: rev, Data: SearchResult{found.Semantic, results, marks}}, err
}

func (s *Service) Message(ctx context.Context, in MessageInput) (application.Read[MessageResult], error) {
	var out MessageResult
	rev, err := s.snapshot(ctx, MessageName, in, func(q db.Querier) error {
		var err error
		out.View, err = message.Get(ctx, q, s.cfg.CacheDir, in.ID)
		if err != nil || out.View == nil {
			return err
		}
		if in.Folders {
			out.Folders, err = triage.Folders(ctx, q, out.View.Account)
		}
		return err
	})
	if err != nil {
		return application.Read[MessageResult]{}, err
	}
	if out.View == nil {
		if in.Tool {
			return application.Read[MessageResult]{}, tool.Error(fmt.Sprintf("no message %d", in.ID))
		}
		return application.Read[MessageResult]{Revision: rev, Data: out}, nil
	}
	if in.Draft {
		out.Draft, err = (&drafts.Store{DB: s.db, Cfg: s.cfg}).ForMessage(ctx, in.ID)
		if err != nil {
			return application.Read[MessageResult]{}, err
		}
	}
	if !in.Tool {
		return application.Read[MessageResult]{Revision: rev, Data: out}, nil
	}
	found := out.View
	headers := map[string]any{}
	for _, name := range message.Headers {
		if v := found.Headers[name]; v != "" {
			headers[name] = tool.Clip(v, 500)
		}
	}
	attachments := make([]any, len(found.Attachments))
	for i, a := range found.Attachments {
		attachments[i] = a.Filename
		if a.Filename == "" {
			attachments[i] = a.ContentType
		}
	}
	body := clip(found.Body, int(in.MaxChars))
	marks := tool.Marks(0)
	if len(headers) > 0 {
		marks |= tool.PII
	}
	if body != "" || len(attachments) > 0 {
		marks |= tool.Messages | tool.Untrusted
	}
	out.Read = &ReadMessage{true, found.ID, found.Account, found.Folder, iso(found.DateTS), headers, body, int64(utf8.RuneCountInString(found.Body)) > in.MaxChars, attachments}
	out.Marks = marks
	return application.Read[MessageResult]{Revision: rev, Data: out}, nil
}
