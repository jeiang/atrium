package rebalance

import (
	"testing"

	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

func TestPatterns(t *testing.T) {
	rxtest.Run(t, map[string]*rx.Regexp{
		"src/atrium/rebalance.py:terms_of:8772f9a4":   termRE,
		"src/atrium/rebalance.py:clean_name:575b7ea7": junkRE,
		"src/atrium/rebalance.py:clean_name:088c52e8": spacesRE,
	})
}
