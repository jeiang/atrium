package kb

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/db"
)

const (
	KindFact   = "fact"
	KindLesson = "lesson"
	KindWiki   = "wiki"

	EdgeMentions = "mentions"
	EdgeSameRun  = "same_run"

	SubjectFactCap = 200
	MapNodeCap     = 50
	MapDepthMax    = 2

	lessonPrefix = "lesson: "
	wikiOrigin   = "notion:"
)

var Kinds = []string{KindFact, KindLesson, KindWiki}

func KindOf(r Record) string {
	switch {
	case strings.HasPrefix(strings.ToLower(r.Title), lessonPrefix):
		return KindLesson
	case r.Origin != nil && strings.HasPrefix(strings.ToLower(*r.Origin), wikiOrigin):
		return KindWiki
	}
	return KindFact
}

func kindWhere(kind string) string {
	lesson := "r.title LIKE 'Lesson: %'"
	wiki := "COALESCE(r.origin, '') LIKE 'notion:%'"
	switch kind {
	case KindLesson:
		return lesson
	case KindWiki:
		return "NOT " + lesson + " AND " + wiki
	case KindFact:
		return "NOT " + lesson + " AND NOT " + wiki
	}
	return "1 = 1"
}

type named struct {
	rec   Record
	names []string
	text  string
}

func nameList(r Record) []string {
	out := []string{}
	for _, candidate := range append([]string{r.Title}, r.Aliases...) {
		if n := normalize(candidate); len(n) >= exactMinChars && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

func (n named) mentions(other named) bool {
	if n.rec.ID == other.rec.ID {
		return false
	}
	for _, name := range other.names {
		if strings.Contains(n.text, name) {
			return true
		}
	}
	return false
}

func (n named) sameRun(other named) bool {
	return n.rec.ID != other.rec.ID && n.rec.SourceRunID != nil && other.rec.SourceRunID != nil && *n.rec.SourceRunID == *other.rec.SourceRunID
}

func loadNamed(ctx context.Context, q db.Querier) ([]named, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+columns+" FROM kb_records r ORDER BY r.updated_at DESC, r.id DESC")
	if err != nil {
		return nil, err
	}
	records, err := db.Collect(rows, func(r *sql.Rows) (Record, error) { return scan(r) })
	if err != nil {
		return nil, err
	}
	out := make([]named, len(records))
	for i, r := range records {
		out[i] = named{rec: r, names: nameList(r), text: normalize(r.Title + " " + strings.Join(r.Aliases, " ") + " " + r.Body)}
	}
	return out, nil
}

func FactCounts(ctx context.Context, q db.Querier, subjects []Record) (map[int64]int, error) {
	out := make(map[int64]int, len(subjects))
	if len(subjects) == 0 {
		return out, nil
	}
	all, err := loadNamed(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, s := range subjects {
		subject := named{rec: s, names: nameList(s)}
		for _, r := range all {
			if !r.rec.Retracted() && r.mentions(subject) {
				out[s.ID]++
			}
		}
	}
	return out, nil
}

type SubjectPage struct {
	Subject        Record
	Facts          []Record
	FactsTruncated bool
	Mentions       []Record
	SameRun        []Record
}

func Subject(ctx context.Context, q db.Querier, id int64) (*SubjectPage, error) {
	all, err := loadNamed(ctx, q)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(all, func(n named) bool { return n.rec.ID == id })
	if i < 0 {
		return nil, nil
	}
	subject := all[i]
	out := &SubjectPage{Subject: subject.rec, Facts: []Record{}, Mentions: []Record{}, SameRun: []Record{}}
	for _, r := range all {
		if r.mentions(subject) {
			out.Facts = append(out.Facts, r.rec)
		}
		if r.rec.Retracted() {
			continue
		}
		if subject.mentions(r) {
			out.Mentions = append(out.Mentions, r.rec)
		}
		if subject.sameRun(r) {
			out.SameRun = append(out.SameRun, r.rec)
		}
	}
	slices.SortStableFunc(out.Facts, func(a, b Record) int {
		switch {
		case a.CreatedAt != b.CreatedAt && a.CreatedAt > b.CreatedAt:
			return -1
		case a.CreatedAt != b.CreatedAt:
			return 1
		case a.ID > b.ID:
			return -1
		case a.ID < b.ID:
			return 1
		}
		return 0
	})
	if len(out.Facts) > SubjectFactCap {
		out.Facts, out.FactsTruncated = out.Facts[:SubjectFactCap], true
	}
	return out, nil
}

type MapNode struct {
	Record Record
	Depth  int
}

type MapEdge struct {
	From int64
	To   int64
	Kind string
}

type Map struct {
	Nodes     []MapNode
	Edges     []MapEdge
	Truncated bool
}

func Neighborhood(ctx context.Context, q db.Querier, id int64, depth int) (*Map, error) {
	all, err := loadNamed(ctx, q)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(all, func(n named) bool { return n.rec.ID == id })
	if i < 0 {
		return nil, nil
	}
	typed, err := RecordLinks(ctx, q)
	if err != nil {
		return nil, err
	}
	pairs := map[[2]int64]bool{}
	for _, l := range typed {
		to, _ := RecordTarget(l.TargetID)
		pairs[[2]int64{l.RecordID, to}], pairs[[2]int64{to, l.RecordID}] = true, true
	}
	depth = max(1, min(depth, MapDepthMax))
	nodes := []named{all[i]}
	depths := map[int64]int{id: 0}
	out := &Map{}
	for next := 0; next < len(nodes); next++ {
		n := nodes[next]
		if depths[n.rec.ID] >= depth {
			continue
		}
		for _, r := range all {
			if r.rec.Retracted() || depths[r.rec.ID] > 0 || r.rec.ID == id {
				continue
			}
			if !r.mentions(n) && !n.mentions(r) && !n.sameRun(r) && !pairs[[2]int64{n.rec.ID, r.rec.ID}] {
				continue
			}
			if len(nodes) >= MapNodeCap {
				out.Truncated = true
				break
			}
			depths[r.rec.ID] = depths[n.rec.ID] + 1
			nodes = append(nodes, r)
		}
	}
	for a, from := range nodes {
		out.Nodes = append(out.Nodes, MapNode{Record: from.rec, Depth: depths[from.rec.ID]})
		for b, to := range nodes {
			if from.mentions(to) {
				out.Edges = append(out.Edges, MapEdge{From: from.rec.ID, To: to.rec.ID, Kind: EdgeMentions})
			}
			if a < b && from.sameRun(to) {
				out.Edges = append(out.Edges, MapEdge{From: from.rec.ID, To: to.rec.ID, Kind: EdgeSameRun})
			}
		}
	}
	for _, l := range typed {
		to, _ := RecordTarget(l.TargetID)
		if _, ok := depths[l.RecordID]; ok {
			if _, ok := depths[to]; ok {
				out.Edges = append(out.Edges, MapEdge{From: l.RecordID, To: to, Kind: l.Relation})
			}
		}
	}
	return out, nil
}
