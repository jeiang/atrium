package application

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/tokens"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	ListName    application.Name = "knowledge.list"
	DetailName  application.Name = "knowledge.detail"
	ProfileName application.Name = "knowledge.profile"
	SearchName  application.Name = "knowledge.search"
	GetName     application.Name = "knowledge.get"
)

const (
	ScopeWork     = kb.ScopeWork
	ScopePersonal = kb.ScopePersonal
)

var (
	States = []string{"all", "unconfirmed", "pinned", "imported", "retracted"}
	Scopes = []string{ScopeWork, ScopePersonal}
)

var pageScopes = []string{state.Knowledge, state.Assistant, state.Effects, state.Settings}

type Row struct {
	ID        int64
	Title     string
	Scope     string
	Writer    string
	Marks     []string
	Confirmed bool
	Pinned    bool
	Retracted bool
	Updated   float64
}

type Source struct {
	ThreadID int64
	Seq      *int64
	RunID    *int64
	Origin   string
	NotionID string
}

type Record struct {
	ID          int64
	Title       string
	Aliases     []string
	Body        string
	Scope       string
	Private     bool
	Marks       tool.Marks
	MarkNames   []string
	Writer      string
	Pinned      bool
	Confirmed   bool
	Retracted   bool
	SourceRunID *int64
	Source      *Source
	UpdatedAt   float64
}

type HistoryRow struct {
	ID       int64
	TS       float64
	Tool     string
	UndoneAt *float64
}

type ListInput struct {
	Query string
	Scope string
	State string
}

type List struct {
	Filter ListInput
	Rows   []Row
}

type DetailInput struct {
	ID int64
}

type Detail struct {
	Record  Record
	SameRun []Row
	History []HistoryRow
}

type ProfileEntry struct {
	ID       int64
	Title    string
	Body     string
	Reason   string
	InPrompt bool
	Tokens   int64
}

type Profile struct {
	Block   string
	Tokens  int64
	Budget  int64
	Percent int64
	Entries []ProfileEntry
}

type SearchInput struct {
	ThreadID int64
	Query    string
	Scope    string
	Limit    int
	Embed    func(ctx context.Context, texts []string) ([][]float64, error) `json:"-"`
}

type Hit struct {
	Record  Record
	Snippet string
}

type SearchResult struct {
	Hits     []Hit
	Semantic bool
	Marks    tool.Marks
}

type GetInput struct {
	ThreadID int64
	IDs      []int64
}

type GetResult struct {
	Records []Record
	Marks   tool.Marks
}

func DescribeList() application.Definition {
	return application.Describe[ListInput, List](ListName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func DescribeDetail() application.Definition {
	return application.Describe[DetailInput, Detail](DetailName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func DescribeProfile() application.Definition {
	return application.Describe[struct{}, Profile](ProfileName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func DescribeSearch() application.Definition {
	return application.Describe[SearchInput, SearchResult](SearchName, application.Query,
		[]application.Authority{application.Agent}, application.NoEffect, false)
}

func DescribeGet() application.Definition {
	return application.Describe[GetInput, GetResult](GetName, application.Query,
		[]application.Authority{application.Agent}, application.NoEffect, false)
}

func rowOf(r kb.Record) Row {
	return Row{ID: r.ID, Title: r.Title, Scope: r.Scope, Writer: r.Writer, Marks: MarkNames(r.Marks),
		Confirmed: r.Confirmed(), Pinned: r.Pinned, Retracted: r.Retracted(), Updated: r.UpdatedAt}
}

func rowsOf(records []kb.Record) []Row {
	out := make([]Row, len(records))
	for i, r := range records {
		out[i] = rowOf(r)
	}
	return out
}

func sourceOf(r kb.Record) *Source {
	if r.Origin != nil {
		source := &Source{Origin: *r.Origin}
		if strings.HasPrefix(*r.Origin, "notion:") {
			source.NotionID = strings.TrimPrefix(*r.Origin, "notion:")
		}
		return source
	}
	if r.SourceThreadID == nil {
		return nil
	}
	return &Source{ThreadID: *r.SourceThreadID, Seq: r.SourceSeq, RunID: r.SourceRunID}
}

func recordOf(r kb.Record) Record {
	return Record{ID: r.ID, Title: r.Title, Aliases: r.Aliases, Body: r.Body, Scope: r.Scope, Private: r.Private,
		Marks: r.Marks, MarkNames: MarkNames(r.Marks), Writer: r.Writer, Pinned: r.Pinned, Confirmed: r.Confirmed(),
		Retracted: r.Retracted(), SourceRunID: r.SourceRunID, Source: sourceOf(r), UpdatedAt: r.UpdatedAt}
}

func (in ListInput) Normalize() (ListInput, error) {
	in.Query = strings.TrimSpace(in.Query)
	in.Scope = strings.TrimSpace(in.Scope)
	if in.Scope != "" && !slices.Contains(Scopes, in.Scope) {
		return in, invalid("scope", "scope must be work or personal")
	}
	in.State = strings.TrimSpace(in.State)
	if !slices.Contains(States, in.State) {
		in.State = "all"
	}
	return in, nil
}

func (s *Service) List(ctx context.Context, in ListInput) (application.Read[List], error) {
	in, err := in.Normalize()
	if err != nil {
		return application.Read[List]{}, err
	}
	var found []kb.Record
	rev, err := s.snapshot(ctx, string(ListName), pageScopes, in, func(q db.Querier) error {
		found, err = kb.List(ctx, q, kb.ListFilter{Query: in.Query, Scope: in.Scope, State: in.State})
		return err
	})
	if err != nil {
		return application.Read[List]{}, err
	}
	return application.Read[List]{Revision: rev, Data: List{Filter: in, Rows: rowsOf(found)}}, nil
}

func load(ctx context.Context, q db.Querier, id int64) (kb.Record, error) {
	rec, err := kb.Get(ctx, q, id)
	if err != nil {
		return kb.Record{}, err
	}
	if rec == nil {
		return kb.Record{}, notFound("unknown knowledge record")
	}
	return *rec, nil
}

func (s *Service) Detail(ctx context.Context, in DetailInput) (application.Read[Detail], error) {
	var out Detail
	rev, err := s.snapshot(ctx, string(DetailName), pageScopes, in, func(q db.Querier) error {
		rec, err := load(ctx, q, in.ID)
		if err != nil {
			return err
		}
		runID := int64(0)
		if rec.SourceRunID != nil {
			runID = *rec.SourceRunID
		}
		same, err := kb.SameRun(ctx, q, runID, rec.ID)
		if err != nil {
			return err
		}
		history, err := kb.History(ctx, q, rec.ID)
		if err != nil {
			return err
		}
		out = Detail{Record: recordOf(rec), SameRun: rowsOf(same), History: make([]HistoryRow, len(history))}
		for i, h := range history {
			out.History[i] = HistoryRow{ID: h.ID, TS: h.TS, Tool: h.Tool, UndoneAt: h.UndoneAt}
		}
		return nil
	})
	if err != nil {
		return application.Read[Detail]{}, err
	}
	return application.Read[Detail]{Revision: rev, Data: out}, nil
}

func (s *Service) Profile(ctx context.Context) (application.Read[Profile], error) {
	budget := int64(assistant.TuningAgentKbProfileTokens)
	var out Profile
	rev, err := s.snapshot(ctx, string(ProfileName), pageScopes, budget, func(q db.Querier) error {
		block, err := kb.ProfileBlock(ctx, q, 0)
		if err != nil {
			return err
		}
		entries, err := kb.ProfileReview(ctx, q, budget)
		if err != nil {
			return err
		}
		out = Profile{Block: block, Tokens: tokens.Estimate(block), Budget: budget, Entries: make([]ProfileEntry, len(entries))}
		for i, e := range entries {
			out.Entries[i] = ProfileEntry{ID: e.ID, Title: e.Title, Body: e.Body, Reason: e.Reason, InPrompt: e.InPrompt, Tokens: e.Tokens}
		}
		if budget > 0 {
			out.Percent = min(out.Tokens*100/budget, 100)
		}
		return nil
	})
	if err != nil {
		return application.Read[Profile]{}, err
	}
	return application.Read[Profile]{Revision: rev, Data: out}, nil
}

func readerProject(ctx context.Context, q db.Querier, threadID int64) (int64, error) {
	project, err := threads.ProjectOf(ctx, q, threadID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && project == nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return *project, nil
}

func (s *Service) Search(ctx context.Context, in SearchInput) (application.Read[SearchResult], error) {
	if in.Query == "" {
		return application.Read[SearchResult]{}, invalid("query", "query is required")
	}
	if len([]rune(in.Query)) > 200 {
		return application.Read[SearchResult]{}, invalid("query", "query is limited to 200 characters")
	}
	if in.Scope != "" && !slices.Contains(Scopes, in.Scope) {
		return application.Read[SearchResult]{}, invalid("scope", "scope must be work or personal")
	}
	if in.Limit < 1 || in.Limit > 20 {
		return application.Read[SearchResult]{}, invalid("limit", "limit must be between 1 and 20")
	}
	counters, err := s.state.Read(ctx, nil, []string{state.Knowledge, state.Assistant})
	if err != nil {
		return application.Read[SearchResult]{}, err
	}
	project, err := readerProject(ctx, s.db.Read, in.ThreadID)
	if err != nil {
		return application.Read[SearchResult]{}, err
	}
	embed := in.Embed
	embedTexts := func(ctx context.Context, texts []string) ([][]float64, error) {
		if embed == nil {
			return nil, &httpx.ServiceUnavailable{Msg: "no embedder configured"}
		}
		return embed(ctx, texts)
	}
	found, err := kb.Search(ctx, s.db.Read, kb.SearchFilter{
		Query: in.Query, Scope: in.Scope, Limit: in.Limit, ProjectID: project, Now: s.now(), Embed: embedTexts,
	})
	if err != nil {
		return application.Read[SearchResult]{}, err
	}
	out := SearchResult{Hits: make([]Hit, 0, len(found.Hits)), Semantic: found.Semantic}
	for _, h := range found.Hits {
		out.Hits = append(out.Hits, Hit{Record: recordOf(h.Record), Snippet: h.Snippet})
		out.Marks |= h.Marks
	}
	if len(out.Hits) > 0 {
		out.Marks |= tool.Personal
	}
	rev := state.Revision(string(SearchName), counters, []any{in.ThreadID, in.Query, in.Scope, in.Limit}, "agent")
	return application.Read[SearchResult]{Revision: application.Revision(rev), Data: out}, nil
}

func (s *Service) Get(ctx context.Context, in GetInput) (application.Read[GetResult], error) {
	if len(in.IDs) == 0 || len(in.IDs) > 10 {
		return application.Read[GetResult]{}, invalid("ids", "ids must hold between 1 and 10 record ids")
	}
	var found []kb.Record
	tx, err := s.db.Read.BeginTx(ctx, nil)
	if err != nil {
		return application.Read[GetResult]{}, err
	}
	defer func() { _ = tx.Rollback() }()
	counters, err := s.state.Read(ctx, tx, []string{state.Knowledge, state.Assistant})
	if err != nil {
		return application.Read[GetResult]{}, err
	}
	project, err := readerProject(ctx, tx, in.ThreadID)
	if err != nil {
		return application.Read[GetResult]{}, err
	}
	if found, err = kb.Read(ctx, tx, kb.ReadFilter{IDs: in.IDs, ProjectID: project, Now: s.now()}); err != nil {
		return application.Read[GetResult]{}, err
	}
	out := GetResult{Records: make([]Record, 0, len(found))}
	for _, r := range found {
		out.Records = append(out.Records, recordOf(r))
		out.Marks |= r.Marks
	}
	if len(out.Records) > 0 {
		out.Marks |= tool.Personal
	}
	rev := state.Revision(string(GetName), counters, in, "agent")
	return application.Read[GetResult]{Revision: application.Revision(rev), Data: out}, nil
}
