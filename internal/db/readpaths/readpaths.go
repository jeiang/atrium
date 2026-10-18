// Package readpaths is the registry of the queries each module's read paths
// issue, so one harness can run them all against a copy of any atrium.db
// and compare them with what the frozen Python app returned
// (testdata/golden/db/readpaths.json).
package readpaths

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/jeiang/atrium/internal/db"
)

// Path is one read path. Golden names its entry in readpaths.json; Want
// turns that entry into the value Run must return, or is nil when Run
// returns the entry itself.
type Path struct {
	Name   string
	Golden string
	Run    func(ctx context.Context, d *db.DB) (any, error)
	Want   func(recorded json.RawMessage) (any, error)
}

var (
	mu    sync.Mutex
	paths = map[string]Path{}
)

// Register adds p; a module calls it from init. A duplicate name panics.
func Register(p Path) {
	mu.Lock()
	defer mu.Unlock()
	if p.Name == "" || p.Golden == "" || p.Run == nil {
		panic(fmt.Sprintf("readpaths: incomplete path %q", p.Name))
	}
	if _, dup := paths[p.Name]; dup {
		panic("readpaths: duplicate path " + p.Name)
	}
	paths[p.Name] = p
}

// All returns every registered path by name.
func All() []Path {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Path, 0, len(paths))
	for _, p := range paths {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Path) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Expected is p's wanted value for the recorded readpaths.json entries.
func (p Path) Expected(recorded map[string]json.RawMessage) ([]byte, error) {
	raw, ok := recorded[p.Golden]
	if !ok {
		return nil, fmt.Errorf("readpaths: %s: no golden entry %q", p.Name, p.Golden)
	}
	if p.Want == nil {
		return raw, nil
	}
	v, err := p.Want(raw)
	if err != nil {
		return nil, fmt.Errorf("readpaths: %s: %w", p.Name, err)
	}
	return json.Marshal(v)
}

// Result runs p and encodes what it returned.
func (p Path) Result(ctx context.Context, d *db.DB) ([]byte, error) {
	v, err := p.Run(ctx, d)
	if err != nil {
		return nil, fmt.Errorf("readpaths: %s: %w", p.Name, err)
	}
	return json.Marshal(v)
}
