package rxtest

import (
	"testing"

	"github.com/jeiang/atrium/internal/rx"
)

func TestCorpus(t *testing.T) {
	c := Load(t)
	var pass, total, compileErrors int
	for _, p := range c.Patterns {
		t.Run(p.ID, func(t *testing.T) {
			flags, err := p.Flags()
			if err != nil {
				t.Fatal(err)
			}
			re, err := rx.Compile(p.Pattern, flags)
			if p.Error != "" {
				if err == nil {
					t.Fatalf("compiled %q; Python: %s", p.Pattern, p.Error)
				}
				if err.Error() != p.Error {
					t.Errorf("compile error %q, Python %q", err, p.Error)
				}
				compileErrors++
				return
			}
			if err != nil {
				t.Fatalf("compile %q: %v", p.Pattern, err)
			}
			ok, n, failures := c.Check(p.ID, re)
			pass += ok
			total += n
			for _, f := range failures {
				t.Error(f)
			}
			t.Logf("%d/%d records pass", ok, n)
		})
	}
	t.Logf("%d patterns (%d rejected as in Python), %d/%d records pass", len(c.Patterns), compileErrors, pass, total)
}
