package assistantmodule

import (
	"context"
	"net/http"
	"strconv"
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
	RunID int64 `json:"run_id,omitempty"`
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
	ID           int64      `json:"id"`
	At           time.Time  `json:"at"`
	Tool         string     `json:"tool"`
	By           string     `json:"by"`
	PreviousBody *string    `json:"previous_body" api:"nullable"`
	UndoneAt     *time.Time `json:"undone_at" api:"nullable"`
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

type KnowledgeSubjectsRequest struct {
	Query  string     `query:"q" json:"-"`
	Kind   string     `query:"kind" json:"-" api:"enum=fact|lesson|wiki"`
	Scope  string     `query:"scope" json:"-" api:"enum=work|personal"`
	Cursor api.Cursor `query:"cursor" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
}

type KnowledgeSubjectRow struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Kind      string    `json:"kind"`
	Scope     string    `json:"scope"`
	Private   bool      `json:"private"`
	Confirmed bool      `json:"confirmed"`
	Pinned    bool      `json:"pinned"`
	Facts     int       `json:"facts"`
	UpdatedAt time.Time `json:"updated_at"`
}

type KnowledgeSubjects struct {
	Items      []KnowledgeSubjectRow `json:"items"`
	NextCursor *string               `json:"next_cursor" api:"nullable"`
}

type KnowledgeBlock struct {
	Kind string `json:"kind" api:"enum=paragraph|item|numbered|quote|code"`
	Text string `json:"text"`
}

type KnowledgeSection struct {
	Heading string           `json:"heading"`
	Level   int              `json:"level"`
	Blocks  []KnowledgeBlock `json:"blocks"`
}

type KnowledgeFact struct {
	ID           int64            `json:"id"`
	Title        string           `json:"title"`
	Value        string           `json:"value"`
	Kind         string           `json:"kind"`
	Scope        string           `json:"scope"`
	Private      bool             `json:"private"`
	Marks        []string         `json:"marks"`
	Writer       string           `json:"writer"`
	Source       *KnowledgeSource `json:"source" api:"nullable"`
	Confirmed    bool             `json:"confirmed"`
	Superseded   bool             `json:"superseded"`
	SupersededAt *time.Time       `json:"superseded_at" api:"nullable"`
	LearnedAt    time.Time        `json:"learned_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
}

type KnowledgeRelated struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Kind       string `json:"kind"`
	Relation   string `json:"relation"`
	TargetKind string `json:"target_kind" api:"enum=record|task|contact|space"`
	TargetID   string `json:"target_id"`
	Direction  string `json:"direction,omitempty" api:"enum=outgoing|incoming"`
	LinkID     int64  `json:"link_id,omitempty"`
	State      string `json:"state" api:"enum=live|retracted|done|dismissed|archived|missing|unavailable"`
	Address    string `json:"address"`
}

type KnowledgeLinkRequest struct {
	ID         int64  `path:"record_id" json:"-"`
	TargetKind string `json:"target_kind" api:"enum=record|task|contact|space"`
	TargetID   string `json:"target_id"`
	Relation   string `json:"relation" api:"enum=about|part_of|works_with|depends_on|see_also"`
}

type KnowledgeLinkIDRequest struct {
	ID int64 `path:"link_id" json:"-"`
}

type KnowledgeLinkEnd struct {
	Kind       string `json:"kind" api:"enum=record|task|contact|space"`
	ID         string `json:"id"`
	Title      string `json:"title"`
	RecordKind string `json:"record_kind,omitempty"`
	State      string `json:"state" api:"enum=live|retracted|done|dismissed|archived|missing|unavailable"`
	Address    string `json:"address"`
}

type KnowledgeLink struct {
	ID        int64            `json:"id"`
	Relation  string           `json:"relation"`
	Writer    string           `json:"writer" api:"enum=operator|assistant"`
	RunID     *int64           `json:"run_id" api:"nullable"`
	CreatedAt time.Time        `json:"created_at"`
	Removed   bool             `json:"removed"`
	Source    KnowledgeLinkEnd `json:"source"`
	Target    KnowledgeLinkEnd `json:"target"`
}

type KnowledgeLinks struct {
	RecordID int64           `json:"record_id"`
	Outgoing []KnowledgeLink `json:"outgoing"`
	Incoming []KnowledgeLink `json:"incoming"`
}

type KnowledgeSubjectPage struct {
	Subject        KnowledgeRecord    `json:"subject"`
	Kind           string             `json:"kind"`
	CreatedAt      time.Time          `json:"created_at"`
	Sections       []KnowledgeSection `json:"sections"`
	Facts          []KnowledgeFact    `json:"facts"`
	FactsTruncated bool               `json:"facts_truncated"`
	Related        []KnowledgeRelated `json:"related"`
}

type KnowledgeMapRequest struct {
	ID    int64  `path:"record_id" json:"-"`
	Depth *int64 `query:"depth" json:"-" api:"min=1,max=2"`
}

type KnowledgeMapNode struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Kind      string `json:"kind"`
	Scope     string `json:"scope"`
	Private   bool   `json:"private"`
	Confirmed bool   `json:"confirmed"`
	Depth     int    `json:"depth"`
}

type KnowledgeMapEdge struct {
	From int64  `json:"from"`
	To   int64  `json:"to"`
	Kind string `json:"kind"`
}

type KnowledgeMap struct {
	Center    int64              `json:"center"`
	Nodes     []KnowledgeMapNode `json:"nodes"`
	Edges     []KnowledgeMapEdge `json:"edges"`
	Cap       int                `json:"cap"`
	Truncated bool               `json:"truncated"`
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
				return knowledgeDetail(read, err)
			}),
		api.Query(d, kbapp.DescribeInstructions(), application.APIShape{Area: area, Errors: missing},
			"GET /knowledge/instructions", func(ctx context.Context, _ struct{}) (application.Read[KnowledgeDetail], error) {
				read, err := svc().Instructions(ctx)
				return knowledgeDetail(read, err)
			}),
		api.Query(d, kbapp.DescribeSubjects(), application.APIShape{Area: area, Errors: []application.Code{application.Invalid}},
			"GET /knowledge/subjects", func(ctx context.Context, in KnowledgeSubjectsRequest) (application.Read[KnowledgeSubjects], error) {
				list := kbapp.SubjectsInput{Query: in.Query, Kind: in.Kind, Scope: in.Scope, Limit: int(in.Limit) + 1}
				if in.Cursor.Set() {
					var last knowledgeCursor
					if err := in.Cursor.Last(&last); err != nil {
						return application.Read[KnowledgeSubjects]{}, err
					}
					list.After = &kb.ListKey{UpdatedAt: last.UpdatedAt, ID: last.ID}
				}
				read, err := svc().Subjects(ctx, list)
				if err != nil {
					return application.Read[KnowledgeSubjects]{}, err
				}
				rows := read.Data.Rows
				page := KnowledgeSubjects{Items: []KnowledgeSubjectRow{}}
				if len(rows) > int(in.Limit) {
					rows = rows[:in.Limit]
					last := rows[len(rows)-1]
					page.NextCursor = in.Cursor.Next(knowledgeCursor{UpdatedAt: last.Updated, ID: last.ID})
				}
				for _, r := range rows {
					page.Items = append(page.Items, KnowledgeSubjectRow{ID: r.ID, Title: r.Title, Kind: r.Kind, Scope: r.Scope, Private: r.Private,
						Confirmed: r.Confirmed, Pinned: r.Pinned, Facts: r.Facts, UpdatedAt: seconds(r.Updated)})
				}
				return application.Read[KnowledgeSubjects]{Revision: read.Revision, Data: page}, nil
			}),
		api.Query(d, kbapp.DescribeSubject(), application.APIShape{Area: area, Errors: missing},
			"GET /knowledge/{record_id}/page", func(ctx context.Context, in KnowledgeIDRequest) (application.Read[KnowledgeSubjectPage], error) {
				read, err := svc().Subject(ctx, kbapp.SubjectInput{ID: in.ID})
				if err != nil {
					return application.Read[KnowledgeSubjectPage]{}, err
				}
				return application.Read[KnowledgeSubjectPage]{Revision: read.Revision, Data: knowledgeSubjectPage(read.Data)}, nil
			}),
		api.Query(d, kbapp.DescribeMap(), application.APIShape{Area: area, Errors: missing},
			"GET /knowledge/{record_id}/map", func(ctx context.Context, in KnowledgeMapRequest) (application.Read[KnowledgeMap], error) {
				input := kbapp.MapInput{ID: in.ID}
				if in.Depth != nil {
					input.Depth = int(*in.Depth)
				}
				read, err := svc().Map(ctx, input)
				if err != nil {
					return application.Read[KnowledgeMap]{}, err
				}
				m := read.Data
				out := KnowledgeMap{Center: m.Center, Nodes: make([]KnowledgeMapNode, len(m.Nodes)), Edges: make([]KnowledgeMapEdge, len(m.Edges)), Cap: m.Cap, Truncated: m.Truncated}
				for i, n := range m.Nodes {
					out.Nodes[i] = KnowledgeMapNode{ID: n.ID, Title: n.Title, Kind: n.Kind, Scope: n.Scope, Private: n.Private, Confirmed: n.Confirmed, Depth: n.Depth}
				}
				for i, e := range m.Edges {
					out.Edges[i] = KnowledgeMapEdge{From: e.From, To: e.To, Kind: e.Kind}
				}
				return application.Read[KnowledgeMap]{Revision: read.Revision, Data: out}, nil
			}),
		api.Query(d, kbapp.DescribeLinks(), application.APIShape{Area: area, Errors: missing},
			"GET /knowledge/{record_id}/links", func(ctx context.Context, in KnowledgeIDRequest) (application.Read[KnowledgeLinks], error) {
				read, err := svc().Links(ctx, kbapp.LinksInput{ID: in.ID})
				if err != nil {
					return application.Read[KnowledgeLinks]{}, err
				}
				out := KnowledgeLinks{RecordID: read.Data.Record, Outgoing: knowledgeLinks(read.Data.Outgoing), Incoming: knowledgeLinks(read.Data.Incoming)}
				return application.Read[KnowledgeLinks]{Revision: read.Revision, Data: out}, nil
			}),
		api.Command(d, kbapp.DescribeLink(), application.APIShape{Area: area, Success: http.StatusCreated, Errors: []application.Code{application.Invalid, application.NotFound, application.Refused}},
			"POST /knowledge/{record_id}/links", func(ctx context.Context, in KnowledgeLinkRequest) (application.Change[KnowledgeLink], error) {
				return knowledgeLinkChange(svc().Link(ctx, kbapp.LinkInput{RecordID: in.ID, TargetKind: in.TargetKind, TargetID: in.TargetID, Relation: in.Relation}))
			}),
		api.Command(d, kbapp.DescribeUnlink(), application.APIShape{Area: area, Body: application.BodyNone, Errors: refusable},
			"POST /knowledge/links/{link_id}/retract", func(ctx context.Context, in KnowledgeLinkIDRequest) (application.Change[KnowledgeLink], error) {
				return knowledgeLinkChange(svc().Unlink(ctx, kbapp.LinkIDInput{ID: in.ID}))
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
				if len(in.IDs) > api.LimitMax {
					return application.Change[KnowledgeBatch]{}, &application.Error{Code: application.Invalid, Message: "Confirm at most " + strconv.Itoa(api.LimitMax) + " records at once.",
						Fields: []application.Field{{Name: "ids", Code: string(application.Invalid), Message: "at most " + strconv.Itoa(api.LimitMax) + " ids"}}}
				}
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

func knowledgeSections(sections []kb.PageSection) []KnowledgeSection {
	out := make([]KnowledgeSection, len(sections))
	for i, s := range sections {
		out[i] = KnowledgeSection{Heading: s.Heading, Level: s.Level, Blocks: make([]KnowledgeBlock, len(s.Blocks))}
		for j, b := range s.Blocks {
			out[i].Blocks[j] = KnowledgeBlock{Kind: b.Kind, Text: b.Text}
		}
	}
	return out
}

func knowledgeSubjectPage(p kbapp.SubjectPage) KnowledgeSubjectPage {
	out := KnowledgeSubjectPage{Subject: knowledgeRecord(p.Subject), Kind: p.Kind, CreatedAt: seconds(p.CreatedAt),
		Sections: knowledgeSections(p.Sections), Facts: make([]KnowledgeFact, len(p.Facts)),
		FactsTruncated: p.FactsTruncated, Related: make([]KnowledgeRelated, len(p.Related))}
	for i, f := range p.Facts {
		r := knowledgeRecord(f.Record)
		out.Facts[i] = KnowledgeFact{ID: r.ID, Title: r.Title, Value: r.Body, Kind: f.Kind, Scope: r.Scope, Private: r.Private, Marks: r.Marks,
			Writer: r.Writer, Source: r.Source, Confirmed: r.Confirmed, Superseded: r.Retracted, LearnedAt: seconds(f.CreatedAt), UpdatedAt: r.UpdatedAt}
		if f.Retracted != nil {
			at := seconds(*f.Retracted)
			out.Facts[i].SupersededAt = &at
		}
	}
	for i, r := range p.Related {
		out.Related[i] = KnowledgeRelated{ID: r.ID, Title: r.Title, Kind: r.Kind, Relation: r.Relation, TargetKind: r.TargetKind, TargetID: r.TargetID,
			Direction: r.Direction, LinkID: r.LinkID, State: r.State, Address: r.Address}
	}
	return out
}

func knowledgeLinkEnd(e kbapp.Endpoint) KnowledgeLinkEnd {
	return KnowledgeLinkEnd{Kind: e.Kind, ID: e.ID, Title: e.Title, RecordKind: e.RecordKind, State: e.State, Address: e.Address}
}

func knowledgeLink(l kbapp.Link) KnowledgeLink {
	return KnowledgeLink{ID: l.ID, Relation: l.Relation, Writer: l.Writer, RunID: l.RunID, CreatedAt: seconds(l.CreatedAt), Removed: l.Retracted != nil,
		Source: knowledgeLinkEnd(l.Source), Target: knowledgeLinkEnd(l.Target)}
}

func knowledgeLinks(links []kbapp.Link) []KnowledgeLink {
	out := make([]KnowledgeLink, len(links))
	for i, l := range links {
		out[i] = knowledgeLink(l)
	}
	return out
}

func knowledgeLinkChange(change application.Change[kbapp.Link], err error) (application.Change[KnowledgeLink], error) {
	if err != nil {
		return application.Change[KnowledgeLink]{}, err
	}
	return application.Change[KnowledgeLink]{Data: knowledgeLink(change.Data), AuditID: change.AuditID, EffectIDs: change.EffectIDs}, nil
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

func knowledgeDetail(read application.Read[kbapp.Detail], err error) (application.Read[KnowledgeDetail], error) {
	if err != nil {
		return application.Read[KnowledgeDetail]{}, err
	}
	out := KnowledgeDetail{Record: knowledgeRecord(read.Data.Record), SameRun: make([]KnowledgeRow, len(read.Data.SameRun)), History: make([]KnowledgeHistory, len(read.Data.History))}
	for i, r := range read.Data.SameRun {
		out.SameRun[i] = knowledgeRow(r)
	}
	for i, h := range read.Data.History {
		out.History[i] = KnowledgeHistory{ID: h.ID, At: seconds(h.TS), Tool: h.Tool, By: h.By, PreviousBody: h.PreviousBody}
		if h.UndoneAt != nil {
			at := seconds(*h.UndoneAt)
			out.History[i].UndoneAt = &at
		}
	}
	return application.Read[KnowledgeDetail]{Revision: read.Revision, Data: out}, nil
}
