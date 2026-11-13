package assistantmodule

import (
	"context"
	"net/http"
	"time"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/kb"
	kbapp "github.com/jeiang/atrium/internal/kb/application"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/web"
)

type KnowledgeListRequest struct {
	Query  string     `query:"q" json:"-"`
	Scope  string     `query:"scope" json:"-"`
	State  string     `query:"state" json:"-" api:"enum=all|unconfirmed|pinned|imported|retracted"`
	Cursor api.Cursor `query:"cursor" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
}

type KnowledgeIDRequest struct {
	ID int64 `path:"record_id" json:"-"`
}

type KnowledgeFields struct {
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Aliases []string `json:"aliases,omitempty"`
	Scope   string   `json:"scope" api:"enum=work|personal"`
	Private bool     `json:"private,omitempty"`
	Marks   []string `json:"marks,omitempty"`
	Pinned  bool     `json:"pinned,omitempty"`
}

type KnowledgeEditRequest struct {
	ID      int64     `path:"record_id" json:"-"`
	Title   *string   `json:"title,omitempty"`
	Body    *string   `json:"body,omitempty"`
	Aliases *[]string `json:"aliases,omitempty"`
	Scope   *string   `json:"scope,omitempty" api:"enum=work|personal"`
	Private *bool     `json:"private,omitempty"`
	Marks   *[]string `json:"marks,omitempty"`
	Pinned  *bool     `json:"pinned,omitempty"`
}

type KnowledgeSelectedRequest struct {
	IDs []int64 `json:"ids"`
}

type KnowledgeMatchingRequest struct {
	Query string `json:"q,omitempty"`
	Scope string `json:"scope,omitempty"`
	State string `json:"state" api:"enum=unconfirmed|imported"`
}

type KnowledgeRunRequest struct {
	RunID int64 `json:"run_id"`
}

type KnowledgeImportRequest struct {
	DryRun bool `json:"dry_run,omitempty"`
}

type KnowledgeRow struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Scope     string    `json:"scope"`
	Writer    string    `json:"writer"`
	Marks     []string  `json:"marks"`
	Confirmed bool      `json:"confirmed"`
	Pinned    bool      `json:"pinned"`
	Retracted bool      `json:"retracted"`
	UpdatedAt time.Time `json:"updated_at"`
}

type KnowledgePage struct {
	Items      []KnowledgeRow `json:"items"`
	NextCursor *string        `json:"next_cursor" api:"nullable"`
}

type KnowledgeSource struct {
	ThreadID int64  `json:"thread_id,omitempty"`
	Seq      *int64 `json:"seq,omitempty"`
	RunID    *int64 `json:"run_id,omitempty"`
	Origin   string `json:"origin,omitempty"`
	NotionID string `json:"notion_id,omitempty"`
}

type KnowledgeRecord struct {
	ID        int64            `json:"id"`
	Title     string           `json:"title"`
	Aliases   []string         `json:"aliases"`
	Body      string           `json:"body"`
	Scope     string           `json:"scope"`
	Private   bool             `json:"private"`
	Marks     []string         `json:"marks"`
	Writer    string           `json:"writer"`
	Pinned    bool             `json:"pinned"`
	Confirmed bool             `json:"confirmed"`
	Retracted bool             `json:"retracted"`
	Source    *KnowledgeSource `json:"source" api:"nullable"`
	UpdatedAt time.Time        `json:"updated_at"`
}

type KnowledgeHistory struct {
	ID       int64      `json:"id"`
	At       time.Time  `json:"at"`
	Tool     string     `json:"tool"`
	UndoneAt *time.Time `json:"undone_at" api:"nullable"`
}

type KnowledgeDetail struct {
	Record  KnowledgeRecord    `json:"record"`
	SameRun []KnowledgeRow     `json:"same_run"`
	History []KnowledgeHistory `json:"history"`
}

type KnowledgeProfileEntry struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Reason   string `json:"reason"`
	InPrompt bool   `json:"in_prompt"`
	Tokens   int64  `json:"tokens"`
}

type KnowledgeProfile struct {
	Block   string                  `json:"block"`
	Tokens  int64                   `json:"tokens"`
	Budget  int64                   `json:"budget"`
	Percent int64                   `json:"percent"`
	Entries []KnowledgeProfileEntry `json:"entries"`
}

type KnowledgeChanged struct {
	ID     int64            `json:"id"`
	Record *KnowledgeRecord `json:"record" api:"nullable"`
}

type KnowledgeBatchItem struct {
	ID      int64            `json:"id"`
	Code    application.Code `json:"code,omitempty"`
	Message string           `json:"message,omitempty"`
}

type KnowledgeBatch struct {
	Committed []KnowledgeBatchItem `json:"committed"`
	Refused   []KnowledgeBatchItem `json:"refused"`
	Failed    []KnowledgeBatchItem `json:"failed"`
}

type knowledgeCursor struct {
	UpdatedAt float64 `json:"u"`
	ID        int64   `json:"id"`
}

func (m Module) knowledgeRoutes(d *web.Deps) []application.Binding {
	svc := func() *kbapp.Service { return m.knowledgeService(d) }
	const area application.Area = "knowledge"
	missing := []application.Code{application.NotFound}
	refusable := []application.Code{application.NotFound, application.Refused, application.Conflict}
	return []application.Binding{
		api.Query(d, kbapp.DescribeList(), application.APIShape{Area: area, Errors: []application.Code{application.Invalid}},
			"GET /knowledge", func(ctx context.Context, in KnowledgeListRequest) (application.Read[KnowledgePage], error) {
				list := kbapp.ListInput{Query: in.Query, Scope: in.Scope, State: in.State, Limit: int(in.Limit) + 1}
				if in.Cursor.Set() {
					var last knowledgeCursor
					if err := in.Cursor.Last(&last); err != nil {
						return application.Read[KnowledgePage]{}, err
					}
					list.After = &kb.ListKey{UpdatedAt: last.UpdatedAt, ID: last.ID}
				}
				read, err := svc().List(ctx, list)
				if err != nil {
					return application.Read[KnowledgePage]{}, err
				}
				rows := read.Data.Rows
				page := KnowledgePage{Items: []KnowledgeRow{}}
				if len(rows) > int(in.Limit) {
					rows = rows[:in.Limit]
					last := rows[len(rows)-1]
					page.NextCursor = in.Cursor.Next(knowledgeCursor{UpdatedAt: last.Updated, ID: last.ID})
				}
				for _, r := range rows {
					page.Items = append(page.Items, knowledgeRow(r))
				}
				return application.Read[KnowledgePage]{Revision: read.Revision, Data: page}, nil
			}),
		api.Query(d, kbapp.DescribeProfile(), application.APIShape{Area: area},
			"GET /knowledge/profile", func(ctx context.Context, _ struct{}) (application.Read[KnowledgeProfile], error) {
				read, err := svc().Profile(ctx)
				if err != nil {
					return application.Read[KnowledgeProfile]{}, err
				}
				p := read.Data
				out := KnowledgeProfile{Block: p.Block, Tokens: p.Tokens, Budget: p.Budget, Percent: p.Percent, Entries: make([]KnowledgeProfileEntry, len(p.Entries))}
				for i, e := range p.Entries {
					out.Entries[i] = KnowledgeProfileEntry{ID: e.ID, Title: e.Title, Body: e.Body, Reason: e.Reason, InPrompt: e.InPrompt, Tokens: e.Tokens}
				}
				return application.Read[KnowledgeProfile]{Revision: read.Revision, Data: out}, nil
			}),
		api.Query(d, kbapp.DescribeDetail(), application.APIShape{Area: area, Errors: missing},
			"GET /knowledge/{record_id}", func(ctx context.Context, in KnowledgeIDRequest) (application.Read[KnowledgeDetail], error) {
				read, err := svc().Detail(ctx, kbapp.DetailInput{ID: in.ID})
				if err != nil {
					return application.Read[KnowledgeDetail]{}, err
				}
				out := KnowledgeDetail{Record: knowledgeRecord(read.Data.Record), SameRun: make([]KnowledgeRow, len(read.Data.SameRun)), History: make([]KnowledgeHistory, len(read.Data.History))}
				for i, r := range read.Data.SameRun {
					out.SameRun[i] = knowledgeRow(r)
				}
				for i, h := range read.Data.History {
					out.History[i] = KnowledgeHistory{ID: h.ID, At: seconds(h.TS), Tool: h.Tool}
					if h.UndoneAt != nil {
						at := seconds(*h.UndoneAt)
						out.History[i].UndoneAt = &at
					}
				}
				return application.Read[KnowledgeDetail]{Revision: read.Revision, Data: out}, nil
			}),
		api.Command(d, kbapp.DescribeCreate(), application.APIShape{Area: area, Success: http.StatusCreated, Errors: []application.Code{application.Invalid}},
			"POST /knowledge", func(ctx context.Context, in KnowledgeFields) (application.Change[KnowledgeChanged], error) {
				record, err := in.input()
				if err != nil {
					return application.Change[KnowledgeChanged]{}, err
				}
				return knowledgeChange(svc().Create(ctx, record))
			}),
		api.Command(d, kbapp.DescribeEdit(), application.APIShape{Area: area, Errors: []application.Code{application.Invalid, application.NotFound, application.Refused}},
			"PATCH /knowledge/{record_id}", func(ctx context.Context, in KnowledgeEditRequest) (application.Change[KnowledgeChanged], error) {
				stored, err := svc().Detail(ctx, kbapp.DetailInput{ID: in.ID})
				if err != nil {
					return application.Change[KnowledgeChanged]{}, err
				}
				record, err := in.merge(stored.Data.Record)
				if err != nil {
					return application.Change[KnowledgeChanged]{}, err
				}
				return knowledgeChange(svc().Edit(ctx, kbapp.EditInput{ID: in.ID, RecordInput: record}))
			}),
		api.Command(d, kbapp.DescribeConfirm(), application.APIShape{Area: area, Body: application.BodyNone, Errors: refusable},
			"POST /knowledge/{record_id}/confirm", func(ctx context.Context, in KnowledgeIDRequest) (application.Change[KnowledgeChanged], error) {
				return knowledgeChange(svc().Confirm(ctx, kbapp.IDInput{ID: in.ID}))
			}),
		api.Command(d, kbapp.DescribeRetract(), application.APIShape{Area: area, Body: application.BodyNone, Errors: refusable},
			"POST /knowledge/{record_id}/retract", func(ctx context.Context, in KnowledgeIDRequest) (application.Change[KnowledgeChanged], error) {
				return knowledgeChange(svc().Retract(ctx, kbapp.IDInput{ID: in.ID}))
			}),
		api.Command(d, kbapp.DescribeRestore(), application.APIShape{Area: area, Body: application.BodyNone, Errors: refusable},
			"POST /knowledge/{record_id}/restore", func(ctx context.Context, in KnowledgeIDRequest) (application.Change[KnowledgeChanged], error) {
				return knowledgeChange(svc().Restore(ctx, kbapp.IDInput{ID: in.ID}))
			}),
		api.Command(d, kbapp.DescribePurge(), application.APIShape{Area: area, Body: application.BodyNone, Success: http.StatusNoContent, Errors: refusable},
			"DELETE /knowledge/{record_id}", func(ctx context.Context, in KnowledgeIDRequest) (application.Change[KnowledgeChanged], error) {
				return knowledgeChange(svc().Purge(ctx, kbapp.IDInput{ID: in.ID}))
			}),
		api.Command(d, kbapp.DescribeConfirmSelected(), application.APIShape{Area: area, Errors: []application.Code{application.Invalid}},
			"POST /knowledge/confirm", func(ctx context.Context, in KnowledgeSelectedRequest) (application.Change[KnowledgeBatch], error) {
				return knowledgeBatch(svc().ConfirmSelected(ctx, kbapp.SelectedInput{IDs: in.IDs}))
			}),
		api.Command(d, kbapp.DescribeConfirmMatching(), application.APIShape{Area: area, Errors: []application.Code{application.Invalid}},
			"POST /knowledge/confirm-matching", func(ctx context.Context, in KnowledgeMatchingRequest) (application.Change[KnowledgeBatch], error) {
				return knowledgeBatch(svc().ConfirmMatching(ctx, kbapp.ListInput{Query: in.Query, Scope: in.Scope, State: in.State}))
			}),
		api.Command(d, kbapp.DescribeRetractRun(), application.APIShape{Area: area},
			"POST /knowledge/retract-run", func(ctx context.Context, in KnowledgeRunRequest) (application.Change[KnowledgeBatch], error) {
				return knowledgeBatch(svc().RetractRun(ctx, kbapp.RunInput{RunID: in.RunID}))
			}),
		api.Command(d, kbapp.DescribeImport(), application.APIShape{Area: area},
			"POST /knowledge/import", func(ctx context.Context, in KnowledgeImportRequest) (application.Change[kb.ImportStats], error) {
				return svc().Import(ctx, kbapp.ImportInput{DryRun: in.DryRun})
			}),
	}
}

func seconds(v float64) time.Time {
	return time.Unix(0, int64(v*float64(time.Second))).UTC()
}

func strs(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func knowledgeRow(r kbapp.Row) KnowledgeRow {
	return KnowledgeRow{ID: r.ID, Title: r.Title, Scope: r.Scope, Writer: r.Writer, Marks: strs(r.Marks), Confirmed: r.Confirmed,
		Pinned: r.Pinned, Retracted: r.Retracted, UpdatedAt: seconds(r.Updated)}
}

func knowledgeRecord(r kbapp.Record) KnowledgeRecord {
	out := KnowledgeRecord{ID: r.ID, Title: r.Title, Aliases: strs(r.Aliases), Body: r.Body, Scope: r.Scope, Private: r.Private,
		Marks: strs(r.MarkNames), Writer: r.Writer, Pinned: r.Pinned, Confirmed: r.Confirmed, Retracted: r.Retracted, UpdatedAt: seconds(r.UpdatedAt)}
	if s := r.Source; s != nil {
		out.Source = &KnowledgeSource{ThreadID: s.ThreadID, Seq: s.Seq, RunID: s.RunID, Origin: s.Origin, NotionID: s.NotionID}
	}
	return out
}

func (f KnowledgeFields) input() (kbapp.RecordInput, error) {
	marks, err := marksOf(f.Marks)
	if err != nil {
		return kbapp.RecordInput{}, err
	}
	return kbapp.RecordInput{Title: f.Title, Body: f.Body, Aliases: f.Aliases, Scope: f.Scope, Private: f.Private, Marks: marks, Pinned: f.Pinned}, nil
}

func (in KnowledgeEditRequest) merge(rec kbapp.Record) (kbapp.RecordInput, error) {
	out := kbapp.RecordInput{Title: rec.Title, Body: rec.Body, Aliases: rec.Aliases, Scope: rec.Scope, Private: rec.Private, Marks: rec.Marks, Pinned: rec.Pinned}
	if in.Title != nil {
		out.Title = *in.Title
	}
	if in.Body != nil {
		out.Body = *in.Body
	}
	if in.Aliases != nil {
		out.Aliases = *in.Aliases
	}
	if in.Scope != nil {
		out.Scope = *in.Scope
	}
	if in.Private != nil {
		out.Private = *in.Private
	}
	if in.Pinned != nil {
		out.Pinned = *in.Pinned
	}
	if in.Marks != nil {
		marks, err := marksOf(*in.Marks)
		if err != nil {
			return kbapp.RecordInput{}, err
		}
		out.Marks = marks
	}
	return out, nil
}

func marksOf(names []string) (tool.Marks, error) {
	var marks tool.Marks
	for _, name := range names {
		found := false
		for _, m := range kbapp.Marks {
			if m.Name == name {
				marks |= m.Mark
				found = true
			}
		}
		if !found {
			return 0, &application.Error{Code: application.Invalid, Message: "unknown mark " + name,
				Fields: []application.Field{{Name: "marks", Code: "invalid", Message: "unknown mark " + name}}}
		}
	}
	return marks, nil
}

func knowledgeChange(c application.Change[kbapp.Changed], err error) (application.Change[KnowledgeChanged], error) {
	if err != nil {
		return application.Change[KnowledgeChanged]{}, err
	}
	out := application.Change[KnowledgeChanged]{Data: KnowledgeChanged{ID: c.Data.ID}, EffectIDs: c.EffectIDs, AuditID: c.AuditID}
	if c.Data.Record.Title != "" {
		record := knowledgeRecord(c.Data.Record)
		out.Data.Record = &record
	}
	return out, nil
}

func knowledgeBatch(c application.Change[kbapp.BatchResult], err error) (application.Change[KnowledgeBatch], error) {
	if err != nil {
		return application.Change[KnowledgeBatch]{}, err
	}
	items := func(in []kbapp.BatchItem) []KnowledgeBatchItem {
		out := make([]KnowledgeBatchItem, len(in))
		for i, b := range in {
			out[i] = KnowledgeBatchItem{ID: b.ID, Code: b.Code, Message: b.Message}
		}
		return out
	}
	return application.Change[KnowledgeBatch]{Data: KnowledgeBatch{Committed: items(c.Data.Committed), Refused: items(c.Data.Refused), Failed: items(c.Data.Failed)},
		EffectIDs: c.EffectIDs, AuditID: c.AuditID}, nil
}
