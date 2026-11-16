package day

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/jeiang/atrium/internal/db"
)

type Entry struct {
	Start    time.Time
	End      time.Time
	AllDay   bool
	Title    string
	Location string
	Kind     string
	Ref      string
	Unit     string
}

type Provider interface {
	Kinds() []string
	Entries(ctx context.Context, q db.Querier, from, to time.Time) ([]Entry, error)
}

type Source struct {
	Unit     string
	Provider Provider
}

func Collect(ctx context.Context, q db.Querier, sources []Source, from, to time.Time) ([]Entry, error) {
	var out []Entry
	for _, s := range sources {
		entries, err := s.Provider.Entries(ctx, q, from, to)
		if err != nil {
			return nil, err
		}
		kinds := s.Provider.Kinds()
		for _, e := range entries {
			if !slices.Contains(kinds, e.Kind) {
				return nil, fmt.Errorf("day: unit %s returned an entry of undeclared kind %q", s.Unit, e.Kind)
			}
			e.Unit = s.Unit
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Start.Equal(out[j].Start) {
			return out[i].Start.Before(out[j].Start)
		}
		return out[i].Title < out[j].Title
	})
	return out, nil
}
