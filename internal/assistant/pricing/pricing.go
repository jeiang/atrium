package pricing

import (
	"fmt"
	"slices"

	"github.com/jeiang/atrium/internal/py"
)

const (
	HaikuID  = "claude-haiku-5-5"
	SonnetID = "claude-sonnet-5-5"
	OpusID   = "claude-opus-5-5"

	DefaultModel = HaikuID

	TierLow       = "<=100K"
	TierHigh      = ">100K"
	TierFlat      = "flat"
	TierThreshold = 100_000
	SearchUSD     = 0.01
	million       = 1_000_000
)

type Rates struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheWrite5m float64 `json:"cache_write_5m"`
	CacheWrite1h float64 `json:"cache_write_1h"`
	CacheRead    float64 `json:"cache_read"`
}

type Thinking struct {
	Type      string `json:"type"`
	MaxEffort string `json:"max_effort"`
}

type Model struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	ContextWindow int64      `json:"context_window"`
	MaxOutput     int64      `json:"max_output"`
	Efforts       []string   `json:"efforts"`
	Thinking      []Thinking `json:"thinking"`
	Rates         Rates      `json:"rates"`
	LongRates     *Rates     `json:"long_rates"`
	ForcedTools   bool       `json:"forced_tools"`
}

var allEfforts = []string{"low", "medium", "high", "xhigh", "max"}

var catalog = []Model{
	{
		ID: HaikuID, Name: "Haiku 5.5", ContextWindow: 1_000_000, MaxOutput: 128_000, Efforts: allEfforts, ForcedTools: true,
		Thinking:  []Thinking{{Type: "adaptive"}, {Type: "disabled", MaxEffort: "high"}},
		Rates:     Rates{Input: 0.10, Output: 0.50, CacheWrite5m: 0.125, CacheWrite1h: 0.20, CacheRead: 0.01},
		LongRates: &Rates{Input: 0.50, Output: 2.50, CacheWrite5m: 0.625, CacheWrite1h: 1.00, CacheRead: 0.05},
	},
	{
		ID: SonnetID, Name: "Sonnet 5.5", ContextWindow: 1_000_000, MaxOutput: 128_000, Efforts: allEfforts,
		Thinking: []Thinking{{Type: "adaptive"}, {Type: "between_tools", MaxEffort: "high"}},
		Rates:    Rates{Input: 2, Output: 10, CacheWrite5m: 2.50, CacheWrite1h: 4, CacheRead: 0.10},
	},
	{
		ID: OpusID, Name: "Opus 5.5", ContextWindow: 1_000_000, MaxOutput: 128_000, Efforts: allEfforts,
		Thinking: []Thinking{{Type: "adaptive"}},
		Rates:    Rates{Input: 4, Output: 20, CacheWrite5m: 5, CacheWrite1h: 8, CacheRead: 0.20},
	},
}

type UnknownModel string

func (e UnknownModel) Error() string { return fmt.Sprintf("model %q is not in the catalog", string(e)) }

func Models() []Model { return slices.Clone(catalog) }

func IDs() []string {
	out := make([]string, len(catalog))
	for i, m := range catalog {
		out[i] = m.ID
	}
	return out
}

func Lookup(id string) (Model, error) {
	for _, m := range catalog {
		if m.ID == id {
			return m, nil
		}
	}
	return Model{}, UnknownModel(id)
}

func (m Model) SupportsEffort(effort string) bool { return slices.Contains(m.Efforts, effort) }

func (m Model) Tiered() bool { return m.LongRates != nil }

type Tokens struct{ Input, CacheWrite5m, CacheWrite1h, CacheRead, Output int64 }

func (t Tokens) Prompt() int64 { return t.Input + t.CacheWrite5m + t.CacheWrite1h + t.CacheRead }

func (m Model) tier(t Tokens) (string, Rates) {
	if m.LongRates == nil {
		return TierFlat, m.Rates
	}
	if t.Prompt() > TierThreshold {
		return TierHigh, *m.LongRates
	}
	return TierLow, m.Rates
}

func TierOf(model string, t Tokens) (string, error) {
	m, err := Lookup(model)
	if err != nil {
		return "", err
	}
	tier, _ := m.tier(t)
	return tier, nil
}

func CostUSD(model string, t Tokens, searches int64) (float64, error) {
	m, err := Lookup(model)
	if err != nil {
		return 0, err
	}
	_, r := m.tier(t)
	total := py.Sum([]float64{
		float64(float64(t.Input) * r.Input),
		float64(float64(t.Output) * r.Output),
		float64(float64(t.CacheWrite5m) * r.CacheWrite5m),
		float64(float64(t.CacheWrite1h) * r.CacheWrite1h),
		float64(float64(t.CacheRead) * r.CacheRead),
	})
	return float64(total/million) + float64(float64(searches)*SearchUSD), nil
}
