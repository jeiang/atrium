package memory

import "github.com/jeiang/atrium/internal/assistant/pricing"

const (
	TuningMemoryConcurrency    = 4
	TuningMemoryEffort         = "low"
	TuningMemoryModel          = pricing.HaikuID
	TuningMemoryNodeBytes      = 512
	TuningMemoryTailMessages   = 6
	TuningMemoryVerbatimTokens = 16000
	TuningMemoryViewHigh       = 128000
	TuningMemoryViewLow        = 64000
)
