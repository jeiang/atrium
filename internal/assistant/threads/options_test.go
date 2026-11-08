package threads

import (
	"testing"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/pricing"
)

func TestThinkingValidityMatrix(t *testing.T) {
	cases := []struct {
		model, thinking, effort string
		ok                      bool
	}{
		{pricing.HaikuID, "adaptive", pricing.Low, true},
		{pricing.HaikuID, "adaptive", pricing.Max, true},
		{pricing.HaikuID, "disabled", pricing.High, true},
		{pricing.HaikuID, "disabled", pricing.XHigh, false},
		{pricing.HaikuID, "between_tools", pricing.Low, false},
		{pricing.SonnetID, "between_tools", pricing.High, true},
		{pricing.SonnetID, "between_tools", pricing.XHigh, false},
		{pricing.SonnetID, "adaptive", pricing.Max, true},
		{pricing.OpusID, "adaptive", pricing.Max, true},
		{pricing.OpusID, "between_tools", pricing.Low, false},
		{pricing.OpusID, "disabled", pricing.Low, false},
		{pricing.HaikuID, "nonsense", pricing.Low, false},
	}
	for _, c := range cases {
		o := Options{Model: c.model, Thinking: c.thinking, Effort: c.effort, ContextLimit: assistant.DefaultContextLimit}
		_, err := o.Validate()
		if (err == nil) != c.ok {
			t.Errorf("%s/%s/%s: err = %v, want ok=%v", c.model, c.thinking, c.effort, err, c.ok)
		}
	}
}
