package drafts

import (
	"testing"

	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

func TestPatterns(t *testing.T) {
	rxtest.Run(t, map[string]*rx.Regexp{
		"src/atrium/drafts.py:_clean_line:72b191b6": lineBreakRE,
		"src/atrium/drafts.py:send:b50aef46":        msgidJunkRE,
	})
}
