package smtp

import (
	"testing"

	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

func TestPatterns(t *testing.T) {
	rxtest.Run(t, map[string]*rx.Regexp{"src/atrium/smtp.py:split_addresses:3d7bad05": commaRunRE})
}
