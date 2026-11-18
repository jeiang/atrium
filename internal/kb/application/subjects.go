package application

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/kb"
)

const (
	SubjectsName application.Name = "knowledge.subjects"
	SubjectName  application.Name = "knowledge.subject"
	MapName      application.Name = "knowledge.map"
)

type SubjectsInput struct {
	Query string
	Kind  string
	Scope string
	Limit int
	Sort  string
	After *kb.ListKey
}

type SubjectRow struct {
	ID        int64
	Title     string
	Kind      string
	Scope     string
	Private   bool
	Confirmed bool
	Pinned    bool
	Facts     int
	Updated   float64
	Created   float64
}

type Subjects struct {
	Filter SubjectsInput
	Rows   []SubjectRow
}

type SubjectInput struct {
	ID int64
}

type Fact struct {
	Record    Record
	Kind      string
	CreatedAt float64
	Retracted *float64
}

type Related struct {
	ID         int64
	Title      string
	Kind       string
	Relation   string
	TargetKind string
	TargetID   string
	Direction  string
	LinkID     int64
	Cited      bool
	State      string
	Address    string
}

type SubjectPage struct {
	Subject        Record
	Kind           string
	CreatedAt      float64
	Sections       []kb.PageSection
	Facts          []Fact
	FactsTruncated bool
	Related        []Related
}

type MapInput struct {
	ID    int64
	Depth int
}

type MapNode struct {
	ID        int64
	Title     string
	Kind      string
	Scope     string
	Private   bool
	Confirmed bool
	Depth     int
}

type MapEdge struct {
	From int64
	To   int64
	Kind string
}

type Map struct {
	Center    int64
	Nodes     []MapNode
	Edges     []MapEdge
	Cap       int
	Truncated bool
}

const (
	RelationMentions = "mentions"
	RelationSameRun  = "same_run"
)

func DescribeSubjects() application.Definition {
	return application.Describe[SubjectsInput, Subjects](SubjectsName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func DescribeSubject() application.Definition {
	return application.Describe[SubjectInput, SubjectPage](SubjectName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func DescribeMap() application.Definition {
	return application.Describe[MapInput, Map](MapName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func (in SubjectsInput) Normalize() (SubjectsInput, error) {
	in.Query = strings.TrimSpace(in.Query)
	in.Scope = strings.TrimSpace(in.Scope)
	if in.Scope != "" && !slices.Contains(Scopes, in.Scope) {
		return in, invalid("scope", "scope must be work or personal")
	}
	in.Kind = strings.TrimSpace(in.Kind)
	if in.Kind != "" && !slices.Contains(kb.Kinds, in.Kind) {
		return in, invalid("kind", "kind must be fact, lesson or wiki")
	}
	if in.Sort != "" && !slices.Contains([]string{"changed", "title", "created"}, in.Sort) {
		return in, invalid("sort", "sort must be changed, title or created")
	}
	return in, nil
}

func (s *Service) Subjects(ctx context.Context, in SubjectsInput) (application.Read[Subjects], error) {
	in, err := in.Normalize()
	if err != nil {
		return application.Read[Subjects]{}, err
	}
	var out Subjects
	rev, err := s.snapshot(ctx, string(SubjectsName), pageScopes, in, func(q db.Querier) error {
		found, err := kb.List(ctx, q, kb.ListFilter{Query: in.Query, Titles: true, Scope: in.Scope, Kind: in.Kind, State: "live", Limit: in.Limit, Sort: in.Sort, After: in.After})
		if err != nil {
			return err
		}
		counts, err := kb.FactCounts(ctx, q, found)
		if err != nil {
			return err
		}
		out = Subjects{Filter: in, Rows: make([]SubjectRow, len(found))}
		for i, r := range found {
			out.Rows[i] = SubjectRow{ID: r.ID, Title: r.Title, Kind: kb.KindOf(r), Scope: r.Scope, Private: r.Private,
				Confirmed: r.Confirmed(), Pinned: r.Pinned, Facts: counts[r.ID], Updated: r.UpdatedAt, Created: r.CreatedAt}
		}
		return nil
	})
	if err != nil {
		return application.Read[Subjects]{}, err
	}
	return application.Read[Subjects]{Revision: rev, Data: out}, nil
}

func related(records []kb.Record, relation string) []Related {
	out := make([]Related, len(records))
	for i, r := range records {
		id := strconv.FormatInt(r.ID, 10)
		out[i] = Related{ID: r.ID, Title: r.Title, Kind: kb.KindOf(r), Relation: relation, TargetKind: kb.TargetRecord, TargetID: id, State: EndLive, Address: "/knowledge/" + id}
	}
	return out
}

func typedRelated(links []Link, direction string) []Related {
	out := make([]Related, len(links))
	for i, l := range links {
		other := l.Target
		if direction == DirectionIncoming {
			other = l.Source
		}
		id, _ := strconv.ParseInt(other.ID, 10, 64)
		if other.Kind == kb.TargetContact {
			id = 0
		}
		out[i] = Related{ID: id, Title: other.Title, Kind: other.RecordKind, Relation: l.Relation, TargetKind: other.Kind, TargetID: other.ID,
			Direction: direction, LinkID: l.ID, Cited: l.Cited, State: other.State, Address: other.Address}
	}
	return out
}

func (s *Service) Subject(ctx context.Context, in SubjectInput) (application.Read[SubjectPage], error) {
	var out SubjectPage
	rev, err := s.snapshot(ctx, string(SubjectName), linkScopes, in, func(q db.Querier) error {
		page, err := kb.Subject(ctx, q, in.ID)
		if err != nil {
			return err
		}
		if page == nil {
			return notFound("unknown knowledge record")
		}
		subject := page.Subject
		out = SubjectPage{Subject: recordOf(subject), Kind: kb.KindOf(subject), CreatedAt: subject.CreatedAt,
			Sections: kb.Sections(subject.Body), Facts: make([]Fact, len(page.Facts)), FactsTruncated: page.FactsTruncated,
			Related: append(related(page.Mentions, RelationMentions), related(page.SameRun, RelationSameRun)...)}
		for i, f := range page.Facts {
			out.Facts[i] = Fact{Record: recordOf(f), Kind: kb.KindOf(f), CreatedAt: f.CreatedAt, Retracted: f.RetractedAt}
		}
		typed, err := linksOf(ctx, q, subject, origin(ctx))
		if err != nil {
			return err
		}
		out.Related = slices.Concat(out.Related, typedRelated(typed.Outgoing, DirectionOutgoing), typedRelated(typed.Incoming, DirectionIncoming))
		return nil
	})
	if err != nil {
		return application.Read[SubjectPage]{}, err
	}
	return application.Read[SubjectPage]{Revision: rev, Data: out}, nil
}

func (s *Service) Map(ctx context.Context, in MapInput) (application.Read[Map], error) {
	if in.Depth == 0 {
		in.Depth = kb.MapDepthMax
	}
	var out Map
	rev, err := s.snapshot(ctx, string(MapName), pageScopes, in, func(q db.Querier) error {
		found, err := kb.Neighborhood(ctx, q, in.ID, in.Depth)
		if err != nil {
			return err
		}
		if found == nil {
			return notFound("unknown knowledge record")
		}
		out = Map{Center: in.ID, Nodes: make([]MapNode, len(found.Nodes)), Edges: make([]MapEdge, len(found.Edges)), Cap: kb.MapNodeCap, Truncated: found.Truncated}
		for i, n := range found.Nodes {
			r := n.Record
			out.Nodes[i] = MapNode{ID: r.ID, Title: r.Title, Kind: kb.KindOf(r), Scope: r.Scope, Private: r.Private, Confirmed: r.Confirmed(), Depth: n.Depth}
		}
		for i, e := range found.Edges {
			out.Edges[i] = MapEdge{From: e.From, To: e.To, Kind: e.Kind}
		}
		return nil
	})
	if err != nil {
		return application.Read[Map]{}, err
	}
	return application.Read[Map]{Revision: rev, Data: out}, nil
}
