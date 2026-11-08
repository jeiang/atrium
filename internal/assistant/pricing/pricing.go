package pricing

import (
	"fmt"
	"slices"

	"github.com/jeiang/atrium/internal/numx"
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
	RunCapUSD     float64    `json:"run_cap_usd"`
}

const (
	Low    = "low"
	Medium = "medium"
	High   = "high"
	XHigh  = "xhigh"
	Max    = "max"
)

var Efforts = []string{Low, Medium, High, XHigh, Max}

var catalog = []Model{
	{
		ID: HaikuID, Name: "Haiku 5.5", ContextWindow: 1_000_000, MaxOutput: 128_000, Efforts: Efforts, ForcedTools: true, RunCapUSD: 2,
		Thinking:  []Thinking{{Type: "adaptive"}, {Type: "disabled", MaxEffort: "high"}},
		Rates:     Rates{Input: 0.10, Output: 0.50, CacheWrite5m: 0.125, CacheWrite1h: 0.20, CacheRead: 0.01},
		LongRates: &Rates{Input: 0.50, Output: 2.50, CacheWrite5m: 0.625, CacheWrite1h: 1.00, CacheRead: 0.05},
	},
	{
		ID: SonnetID, Name: "Sonnet 5.5", ContextWindow: 1_000_000, MaxOutput: 128_000, Efforts: Efforts, RunCapUSD: 6,
		Thinking: []Thinking{{Type: "adaptive"}, {Type: "between_tools", MaxEffort: "high"}},
		Rates:    Rates{Input: 2, Output: 10, CacheWrite5m: 2.50, CacheWrite1h: 4, CacheRead: 0.10},
	},
	{
		ID: OpusID, Name: "Opus 5.5", ContextWindow: 1_000_000, MaxOutput: 128_000, Efforts: Efforts, RunCapUSD: 10,
		Thinking: []Thinking{{Type: "adaptive"}},
		Rates:    Rates{Input: 4, Output: 20, CacheWrite5m: 5, CacheWrite1h: 8, CacheRead: 0.20},
	},
}

type UnknownModel string

func (e UnknownModel) Error() string { return fmt.Sprintf("model %q is not in the catalog", string(e)) }

func Models() []Model { return slices.Clone(catalog) }

func MaxContextWindow() int64 {
	var out int64
	for _, m := range catalog {
		out = max(out, m.ContextWindow)
	}
	return out
}

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

func (m Model) SupportsThinking(kind string) bool {
	for _, t := range m.Thinking {
		if t.Type == kind {
			return true
		}
	}
	return false
}

func (m Model) ThinkingMaxEffort(kind string) string {
	for _, t := range m.Thinking {
		if t.Type == kind {
			return t.MaxEffort
		}
	}
	return ""
}

func (m Model) DefaultThinking() string {
	if len(m.Thinking) == 0 {
		return ""
	}
	return m.Thinking[0].Type
}

func EffortRank(effort string) int {
	for i, e := range Efforts {
		if e == effort {
			return i
		}
	}
	return -1
}

func (m Model) ValidThinking(thinking, effort string) bool {
	if !m.SupportsThinking(thinking) {
		return false
	}
	if limit := m.ThinkingMaxEffort(thinking); limit != "" && EffortRank(effort) > EffortRank(limit) {
		return false
	}
	return true
}

func ModelRank(id string) int {
	switch id {
	case HaikuID:
		return 0
	case SonnetID:
		return 1
	case OpusID:
		return 2
	}
	return -1
}

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

func CostUSD(model string, t Tokens, searches int64) (float64, error) {
	m, err := Lookup(model)
	if err != nil {
		return 0, err
	}
	_, r := m.tier(t)
	return costAt(r, t) + float64(float64(searches)*SearchUSD), nil
}

func costAt(r Rates, t Tokens) float64 {
	total := numx.Sum([]float64{
		float64(float64(t.Input) * r.Input),
		float64(float64(t.Output) * r.Output),
		float64(float64(t.CacheWrite5m) * r.CacheWrite5m),
		float64(float64(t.CacheWrite1h) * r.CacheWrite1h),
		float64(float64(t.CacheRead) * r.CacheRead),
	})
	return float64(total / million)
}

func maxRates() Rates {
	var out Rates
	for _, m := range catalog {
		rates := []Rates{m.Rates}
		if m.LongRates != nil {
			rates = append(rates, *m.LongRates)
		}
		for _, r := range rates {
			out.Input = max(out.Input, r.Input)
			out.Output = max(out.Output, r.Output)
			out.CacheWrite5m = max(out.CacheWrite5m, r.CacheWrite5m)
			out.CacheWrite1h = max(out.CacheWrite1h, r.CacheWrite1h)
			out.CacheRead = max(out.CacheRead, r.CacheRead)
		}
	}
	return out
}

func CostOrMax(model string, t Tokens) (float64, string, bool) {
	m, err := Lookup(model)
	if err != nil {
		return costAt(maxRates(), t), TierHigh, true
	}
	tier, r := m.tier(t)
	return costAt(r, t), tier, false
}
