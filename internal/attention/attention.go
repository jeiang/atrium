package attention

import (
	"context"
	"slices"
	"sort"

	"github.com/jeiang/atrium/internal/db"
)

type Group string

const (
	Overdue       Group = "overdue"
	DueSoon       Group = "due_soon"
	NeedsDecision Group = "needs_decision"
	Waiting       Group = "waiting_on_them"
	Upcoming      Group = "upcoming"
)

var groupOrder = []Group{Overdue, DueSoon, NeedsDecision, Waiting, Upcoming}

var groupLabels = map[Group]string{
	Overdue:       "Overdue",
	DueSoon:       "Due soon",
	NeedsDecision: "Needs decision",
	Waiting:       "Waiting on them",
	Upcoming:      "Upcoming",
}

type Action struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

type Item struct {
	Kind     string   `json:"kind"`
	SourceID string   `json:"source_id"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail"`
	Due      float64  `json:"due,omitempty"`
	Created  float64  `json:"created,omitempty"`
	Group    Group    `json:"group"`
	Actions  []Action `json:"actions,omitempty"`
}

type Source interface {
	Name() string
	Items(ctx context.Context, q db.Querier, now float64) ([]Item, error)
}

type GroupResult struct {
	Group Group  `json:"group"`
	Label string `json:"label"`
	Count int    `json:"count"`
	Items []Item `json:"items"`
}

type Result struct {
	Items  []Item        `json:"items"`
	Groups []GroupResult `json:"groups"`
	Counts map[Group]int `json:"counts"`
}

func Query(ctx context.Context, q db.Querier, now float64, sources ...Source) (Result, error) {
	hidden, err := snoozed(ctx, q, now)
	if err != nil {
		return Result{}, err
	}
	var items []Item
	for _, source := range sources {
		found, err := source.Items(ctx, q, now)
		if err != nil {
			return Result{}, err
		}
		for _, item := range found {
			if hidden[item.Kind+"\x00"+item.SourceID] {
				continue
			}
			items = append(items, item)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if ra, rb := groupRank(a.Group), groupRank(b.Group); ra != rb {
			return ra < rb
		}
		if a.Due != b.Due {
			if a.Due == 0 {
				return false
			}
			if b.Due == 0 {
				return true
			}
			return a.Due < b.Due
		}
		if a.Created != b.Created {
			return a.Created < b.Created
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.SourceID < b.SourceID
	})
	result := Result{Items: items, Counts: map[Group]int{}}
	for _, group := range groupOrder {
		var grouped []Item
		for _, item := range items {
			if item.Group == group {
				grouped = append(grouped, item)
			}
		}
		if len(grouped) == 0 {
			continue
		}
		result.Groups = append(result.Groups, GroupResult{Group: group, Label: groupLabels[group], Count: len(grouped), Items: grouped})
		result.Counts[group] = len(grouped)
	}
	return result, nil
}

func groupRank(g Group) int {
	if i := slices.Index(groupOrder, g); i >= 0 {
		return i
	}
	return len(groupOrder)
}
