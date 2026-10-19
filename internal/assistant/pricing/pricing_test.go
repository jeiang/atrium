package pricing

import (
	"math"
	"testing"
)

func TestTiersAndCost(t *testing.T) {
	low := Tokens{Input: 10_000, CacheWrite5m: 20_000, CacheRead: 50_000, Output: 1_000}
	if TierOf(low) != TierLow {
		t.Errorf("tier of %+v = %s", low, TierOf(low))
	}
	want := 0.001 + 0.0025 + 0.0005 + 0.0005 + 0.02
	if got := CostUSD(low, 2); math.Abs(got-want) > 1e-12 {
		t.Errorf("CostUSD(low, 2) = %v, want about %v", got, want)
	}
	high := low
	high.CacheRead = 100_000
	if TierOf(high) != TierHigh {
		t.Errorf("tier of %+v = %s", high, TierOf(high))
	}
	want = (10_000*0.5 + 20_000*0.625 + 100_000*0.05 + 1_000*2.5) / 1e6
	if got := CostUSD(high, 0); math.Abs(got-want) > 1e-12 {
		t.Errorf("CostUSD(high) = %v, want about %v", got, want)
	}
	if TierOf(Tokens{Input: TierThreshold}) != TierLow || TierOf(Tokens{Input: TierThreshold + 1}) != TierHigh {
		t.Error("the threshold is exclusive")
	}
}
