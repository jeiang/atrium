package subagents

import (
	"testing"

	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

func TestRegexCorpus(t *testing.T) {
	rxtest.Run(t, map[string]*rx.Regexp{"src/atrium/agent/subagents.py:NAME": name})
}
