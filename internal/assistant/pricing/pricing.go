package pricing

import "github.com/jeiang/atrium/internal/py"

const (
	Model         = "claude-haiku-5-5"
	TierLow       = "<=100K"
	TierHigh      = ">100K"
	TierThreshold = 100_000
	SearchUSD     = 0.01
	million       = 1_000_000
)

type Rates struct{ Input, Output, CacheWrite5m, CacheWrite1h, CacheRead float64 }

var RatesPerMTok = map[string]Rates{
	TierLow:  {Input: 0.10, Output: 0.50, CacheWrite5m: 0.125, CacheWrite1h: 0.20, CacheRead: 0.01},
	TierHigh: {Input: 0.50, Output: 2.50, CacheWrite5m: 0.625, CacheWrite1h: 1.00, CacheRead: 0.05},
}

type Tokens struct{ Input, CacheWrite5m, CacheWrite1h, CacheRead, Output int64 }

func (t Tokens) Prompt() int64 { return t.Input + t.CacheWrite5m + t.CacheWrite1h + t.CacheRead }

func TierOf(t Tokens) string {
	if t.Prompt() > TierThreshold {
		return TierHigh
	}
	return TierLow
}

// CostUSD is cost_usd: the tokens priced at the tier of their prompt, plus the web searches. The explicit
// float64 conversions keep Go from fusing a multiply into the following add, which Python never does.
func CostUSD(t Tokens, searches int64) float64 {
	r := RatesPerMTok[TierOf(t)]
	total := py.Sum([]float64{
		float64(float64(t.Input) * r.Input),
		float64(float64(t.Output) * r.Output),
		float64(float64(t.CacheWrite5m) * r.CacheWrite5m),
		float64(float64(t.CacheWrite1h) * r.CacheWrite1h),
		float64(float64(t.CacheRead) * r.CacheRead),
	})
	return float64(total/million) + float64(float64(searches)*SearchUSD)
}
