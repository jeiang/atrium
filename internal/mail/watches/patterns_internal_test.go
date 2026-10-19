package watches

import (
	"testing"

	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

func TestPatterns(t *testing.T) {
	patterns := map[string]*rx.Regexp{
		"src/atrium/watches.py:UPS_RE":                 upsRE,
		"src/atrium/watches.py:S10_RE":                 s10RE,
		"src/atrium/watches.py:USPS_RE":                uspsRE,
		"src/atrium/watches.py:DHL_JD_RE":              dhlJDRE,
		"src/atrium/watches.py:FEDEX_RE":               fedexRE,
		"src/atrium/watches.py:DHL_RE":                 dhlRE,
		"src/atrium/watches.py:ORDER_RE":               orderRE,
		"src/atrium/watches.py:merchant_key:618a0867":  nonAlnum,
		"src/atrium/watches.py:clean_package:088c52e8": spaceRuns,
		"src/atrium/watches.py:clean_package:f0ec7669": trackingRef,
		"src/atrium/watches.py:<module>:c92625d2":      statuses[0].pattern,
		"src/atrium/watches.py:<module>:39489f00":      statuses[1].pattern,
		"src/atrium/watches.py:<module>:4e8ce3b3":      statuses[2].pattern,
		"src/atrium/watches.py:<module>:cd61c20e":      statuses[3].pattern,
		"src/atrium/watches.py:<module>:fb0585ba":      statuses[4].pattern,
		"src/atrium/watches.py:<module>:f8dad500":      statuses[5].pattern,
	}
	rxtest.Run(t, patterns)
}
