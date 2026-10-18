package drafts

import (
	"testing"

	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

func TestPatterns(t *testing.T) {
	rxtest.Run(t, map[string]*rx.Regexp{
		"src/atrium/drafts.py:65":  lineBreakRE,
		"src/atrium/drafts.py:299": msgidJunkRE,
	})
}
