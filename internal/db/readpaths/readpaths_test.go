package readpaths_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/db/readpaths"
	"github.com/jeiang/atrium/internal/testutil/golden"

	_ "github.com/jeiang/atrium/internal/mail/drafts"
)

func TestGoldenReadPaths(t *testing.T) {
	paths := readpaths.All()
	if len(paths) == 0 {
		t.Fatal("no read path registered")
	}
	for _, c := range []struct{ db, results string }{
		{"atrium.db", "db/readpaths.json"},
		{"atrium-legacy.db", "db/legacy/migrated/readpaths.json"},
	} {
		t.Run(c.db, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(golden.Dir(), c.results))
			if err != nil {
				t.Fatal(err)
			}
			var recorded map[string]json.RawMessage
			if err := json.Unmarshal(raw, &recorded); err != nil {
				t.Fatal(err)
			}
			d, _ := dbtest.Copy(t, filepath.Join(golden.Dir(), c.db))
			for _, p := range paths {
				t.Run(p.Name, func(t *testing.T) {
					want, err := p.Expected(recorded)
					if err != nil {
						t.Fatal(err)
					}
					got, err := p.Result(t.Context(), d)
					if err != nil {
						t.Fatal(err)
					}
					golden.EqualJSON(t, want, got)
				})
			}
		})
	}
}

func TestRegisterRejectsDuplicates(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("duplicate Register did not panic")
		}
	}()
	p := readpaths.All()[0]
	readpaths.Register(p)
}
