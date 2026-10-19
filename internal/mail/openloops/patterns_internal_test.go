package openloops

import (
	"testing"

	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

func TestPatterns(t *testing.T) {
	rxtest.Run(t, map[string]*rx.Regexp{
		"src/atrium/openloops.py:norm_subject:c0f7446b": subjectPrefix,
		"src/atrium/openloops.py:norm_subject:088c52e8": spaceRuns,
	})
}
