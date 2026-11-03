package attention

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/db"
)

const (
	Overdue       = "overdue"
	DueSoon       = "due_soon"
	NeedsDecision = "needs_decision"
	Info          = "info"

	GroupWaiting  = "waiting"
	GroupUpcoming = "upcoming"

	KindProposal    = "proposal"
	KindDraft       = "draft"
	KindOweReply    = "owe_reply"
	KindCommitment  = "commitment"
	KindWaiting     = "waiting_on_them"
	KindPackage     = "package"
	KindBirthday    = "birthday"
	KindEvent       = "event_candidate"
	KindThreadPlan  = "thread_plan"
	DueSoonDays     = 3
	QueueLimit      = 200
	dateLayout      = "2006-01-02"
	snoozeTableName = "attention_snoozes"
)

var Kinds = []string{KindProposal, KindDraft, KindOweReply, KindCommitment, KindWaiting, KindPackage, KindBirthday, KindEvent, KindThreadPlan}

var classRank = map[string]int{Overdue: 0, DueSoon: 1, NeedsDecision: 2, Info: 3}

type Action struct {
	Label  string `json:"label"`
	Method string `json:"method"`
	Path   string `json:"path"`
}

type Item struct {
	Kind     string   `json:"kind"`
	SourceID string   `json:"source_id"`
	Title    string   `json:"title"`
	Class    string   `json:"class"`
	Due      string   `json:"due,omitempty"`
	TS       float64  `json:"ts"`
	Group    string   `json:"group,omitempty"`
	Actions  []Action `json:"actions"`
}

type Group struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Result struct {
	Items    []Item  `json:"items"`
	Groups   []Group `json:"groups"`
	Waiting  []Item  `json:"waiting"`
	Upcoming []Item  `json:"upcoming"`
}

type Source interface {
	List(ctx context.Context, q db.Querier, now time.Time, loc *time.Location) ([]Item, error)
}

type SourceFunc func(ctx context.Context, q db.Querier, now time.Time, loc *time.Location) ([]Item, error)

func (f SourceFunc) List(ctx context.Context, q db.Querier, now time.Time, loc *time.Location) ([]Item, error) {
	return f(ctx, q, now, loc)
}

func Seconds(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

func Today(now time.Time, loc *time.Location) string { return now.In(loc).Format(dateLayout) }

func DueClass(due string, now time.Time, loc *time.Location) string {
	if due == "" {
		return NeedsDecision
	}
	today := Today(now, loc)
	switch {
	case due < today:
		return Overdue
	case due <= now.In(loc).AddDate(0, 0, DueSoonDays).Format(dateLayout):
		return DueSoon
	}
	return NeedsDecision
}

func Query(ctx context.Context, q db.Querier, sources []Source, now time.Time, loc *time.Location) (Result, error) {
	snoozed, err := snoozedSet(ctx, q, now)
	if err != nil {
		return Result{}, err
	}
	res := Result{Items: []Item{}, Groups: []Group{}, Waiting: []Item{}, Upcoming: []Item{}}
	var all []Item
	for _, s := range sources {
		items, err := s.List(ctx, q, now, loc)
		if err != nil {
			return Result{}, err
		}
		all = append(all, items...)
	}
	slices.SortStableFunc(all, compare)
	counts := map[string]int{}
	for _, it := range all {
		if snoozed[[2]string{it.Kind, it.SourceID}] {
			continue
		}
		if it.Actions == nil {
			it.Actions = []Action{}
		}
		switch it.Group {
		case GroupWaiting:
			res.Waiting = append(res.Waiting, it)
		case GroupUpcoming:
			res.Upcoming = append(res.Upcoming, it)
		default:
			res.Items = append(res.Items, it)
			counts[it.Kind]++
		}
	}
	for kind, n := range counts {
		res.Groups = append(res.Groups, Group{Name: kind, Count: n})
	}
	slices.SortFunc(res.Groups, func(a, b Group) int { return strings.Compare(a.Name, b.Name) })
	return res, nil
}

func compare(a, b Item) int {
	if c := classRank[a.Class] - classRank[b.Class]; c != 0 {
		return c
	}
	switch {
	case a.TS < b.TS:
		return -1
	case a.TS > b.TS:
		return 1
	}
	if c := strings.Compare(a.Kind, b.Kind); c != 0 {
		return c
	}
	return strings.Compare(a.SourceID, b.SourceID)
}

func snoozedSet(ctx context.Context, q db.Querier, now time.Time) (map[[2]string]bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT kind, source_id FROM "+snoozeTableName+" WHERE until > ?", Seconds(now))
	if err != nil {
		return nil, err
	}
	keys, err := db.Collect(rows, func(r *sql.Rows) ([2]string, error) {
		var k [2]string
		return k, r.Scan(&k[0], &k[1])
	})
	out := map[[2]string]bool{}
	for _, k := range keys {
		out[k] = true
	}
	return out, err
}

func Snooze(ctx context.Context, q db.Querier, kind, sourceID string, until, now time.Time) error {
	if !slices.Contains(Kinds, kind) {
		return fmt.Errorf("unknown attention kind %q", kind)
	}
	if sourceID == "" {
		return fmt.Errorf("source id is empty")
	}
	_, err := q.ExecContext(ctx,
		"INSERT INTO "+snoozeTableName+" (kind, source_id, until, created_at) VALUES (?, ?, ?, ?) "+
			"ON CONFLICT(kind, source_id) DO UPDATE SET until = excluded.until, created_at = excluded.created_at",
		kind, sourceID, Seconds(until), Seconds(now))
	return err
}
