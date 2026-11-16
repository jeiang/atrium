package search

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/textsearch"
)

const (
	Name application.Name = "search.all"

	Thread  = "thread"
	Message = "message"
	Record  = "record"
	Task    = "task"
	File    = "file"
	Space   = "space"
	Contact = "contact"

	Failed              = "failed"
	TimedOut            = "timeout"
	SemanticUnavailable = "semantic_unavailable"

	QueryMax     = 200
	LimitDefault = 5
	LimitMax     = 20
	TopLimit     = 5
	SnippetRunes = 160
	Budget       = 4 * time.Second
)

var Types = []string{Thread, Message, Record, Task, File, Space, Contact}

var log = logx.Logger("search")

type Query struct {
	Text   string
	Limit  int
	Offset int
	Now    float64
}

type Env struct {
	DB     *db.DB
	Config *config.State
}

type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type Hit struct {
	Type    string  `json:"type" api:"enum=thread|message|record|task|file|space|contact"`
	ID      int64   `json:"id"`
	Ref     string  `json:"ref,omitempty"`
	Title   string  `json:"title"`
	Detail  string  `json:"detail"`
	Snippet string  `json:"snippet"`
	Matches []Span  `json:"matches"`
	When    float64 `json:"when"`
	Path    string  `json:"path"`
	Private bool    `json:"private"`
}

type Page struct {
	Hits     []Hit
	Degraded bool
}

type Source struct {
	Type string
	Find func(ctx context.Context, env Env, q Query) (Page, error)
}

type Group struct {
	Type  string `json:"type" api:"enum=thread|message|record|task|file|space|contact"`
	Items []Hit  `json:"items"`
	More  bool   `json:"more"`
}

type Partial struct {
	Type   string `json:"type" api:"enum=thread|message|record|task|file|space|contact"`
	Reason string `json:"reason" api:"enum=failed|timeout|semantic_unavailable"`
}

type Request struct {
	Text   string
	Types  []string
	Limit  int
	Offset int
	Now    float64
}

type Result struct {
	Query      string    `json:"query"`
	Top        []Hit     `json:"top"`
	Groups     []Group   `json:"groups"`
	Partial    []Partial `json:"partial"`
	NextCursor *string   `json:"next_cursor"`
}

func Describe() application.Definition {
	return application.Describe[Request, Result](Name, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}

func ParseTypes(raw string) ([]string, bool) {
	if strings.TrimSpace(raw) == "" {
		return slices.Clone(Types), true
	}
	var out []string
	for part := range strings.SplitSeq(raw, ",") {
		part = strings.TrimSpace(part)
		if !slices.Contains(Types, part) {
			return nil, false
		}
		if !slices.Contains(out, part) {
			out = append(out, part)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return slices.Index(Types, a) - slices.Index(Types, b) })
	return out, true
}

type answer struct {
	index int
	page  Page
	err   error
}

func Run(ctx context.Context, env Env, sources []Source, req Request) Result {
	out := Result{Query: req.Text, Top: []Hit{}, Groups: []Group{}, Partial: []Partial{}}
	var chosen []Source
	for _, t := range req.Types {
		for _, s := range sources {
			if s.Type == t {
				chosen = append(chosen, s)
				break
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, Budget)
	defer cancel()
	answers := make(chan answer, len(chosen))
	q := Query{Text: req.Text, Limit: req.Limit + 1, Offset: req.Offset, Now: req.Now}
	for i, s := range chosen {
		go func() {
			page, err := s.Find(ctx, env, q)
			answers <- answer{i, page, err}
		}()
	}
	pages := make([]*answer, len(chosen))
	for range chosen {
		select {
		case a := <-answers:
			pages[a.index] = &a
			continue
		case <-ctx.Done():
		}
		break
	}
	for i, s := range chosen {
		a := pages[i]
		switch {
		case a == nil || a.err != nil && ctx.Err() != nil:
			out.Partial = append(out.Partial, Partial{Type: s.Type, Reason: TimedOut})
			continue
		case a.err != nil:
			log.ErrorContext(ctx, "search source failed", "type", s.Type, logx.Err, a.err)
			out.Partial = append(out.Partial, Partial{Type: s.Type, Reason: Failed})
			continue
		}
		hits := a.page.Hits
		if hits == nil {
			hits = []Hit{}
		}
		g := Group{Type: s.Type, Items: hits[:min(len(hits), req.Limit)], More: len(hits) > req.Limit}
		out.Groups = append(out.Groups, g)
		if a.page.Degraded {
			out.Partial = append(out.Partial, Partial{Type: s.Type, Reason: SemanticUnavailable})
		}
	}
	if len(req.Types) > 1 && req.Offset == 0 {
		out.Top = top(out.Groups)
	}
	return out
}

func top(groups []Group) []Hit {
	out := []Hit{}
	for rank := 0; len(out) < TopLimit; rank++ {
		var tier []Hit
		for _, g := range groups {
			if rank < len(g.Items) {
				tier = append(tier, g.Items[rank])
			}
		}
		if len(tier) == 0 {
			break
		}
		slices.SortStableFunc(tier, func(a, b Hit) int { return cmp.Compare(b.When, a.When) })
		out = append(out, tier[:min(len(tier), TopLimit-len(out))]...)
	}
	return out
}

type builder struct {
	text  strings.Builder
	units int
	spans []Span
	space bool
}

func (b *builder) add(s string, marked bool) {
	start := b.units
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			if b.units > 0 && !b.space {
				b.text.WriteByte(' ')
				b.units++
				b.space = true
			}
			continue
		}
		if r == utf8.RuneError {
			continue
		}
		b.text.WriteRune(r)
		b.units += utf16.RuneLen(r)
		b.space = false
	}
	if marked && b.units > start {
		b.spans = append(b.spans, Span{Start: start, End: b.units})
	}
}

func (b *builder) done() (string, []Span) {
	text := b.text.String()
	if b.space {
		text = strings.TrimSuffix(text, " ")
	}
	if b.spans == nil {
		b.spans = []Span{}
	}
	return text, b.spans
}

func Marked(marked string) (string, []Span) {
	var b builder
	for _, p := range textsearch.SnippetParts(marked) {
		b.add(p.Text, p.Highlighted)
	}
	return b.done()
}

func Excerpt(text, query string) (string, []Span) {
	runes := []rune(text)
	needle := []rune(strings.TrimSpace(query))
	var found [][2]int
	if len(needle) > 0 {
		for i := 0; i+len(needle) <= len(runes); {
			if strings.EqualFold(string(runes[i:i+len(needle)]), string(needle)) {
				found = append(found, [2]int{i, i + len(needle)})
				i += len(needle)
				continue
			}
			i++
		}
	}
	from := 0
	if len(found) > 0 {
		from = max(found[0][0]-SnippetRunes/4, 0)
	}
	to := min(from+SnippetRunes, len(runes))
	var b builder
	if from > 0 {
		b.add("…", false)
	}
	at := from
	for _, f := range found {
		if f[0] < at || f[1] > to {
			continue
		}
		b.add(string(runes[at:f[0]]), false)
		b.add(string(runes[f[0]:f[1]]), true)
		at = f[1]
	}
	b.add(string(runes[at:to]), false)
	if to < len(runes) {
		b.add("…", false)
	}
	return b.done()
}
