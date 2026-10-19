package tpl

import (
	"testing"

	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

func TestPatterns(t *testing.T) {
	const file = "src/atrium/web/markup.py:"
	rxtest.Run(t, map[string]*rx.Regexp{file + "EMPTY_HEAD": emptyHeadRe, file + "TASK": taskRe, file + "HREF": hrefRe})
}
