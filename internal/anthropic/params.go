package anthropic

import (
	"bytes"
	"encoding/json"
)

const (
	BetaContextManagement = "context-management-2025-06-27"
	BetaCompaction        = "compact-2026-01-12"
)

const (
	StopEndTurn    = "end_turn"
	StopToolUse    = "tool_use"
	StopPauseTurn  = "pause_turn"
	StopCompaction = "compaction"
	StopMaxTokens  = "max_tokens"
)

type Params struct {
	Model             string            `json:"model"`
	MaxTokens         int64             `json:"max_tokens"`
	Thinking          *Thinking         `json:"thinking,omitempty"`
	OutputConfig      *OutputConfig     `json:"output_config,omitempty"`
	System            json.RawMessage   `json:"system,omitempty"`
	ContextManagement json.RawMessage   `json:"context_management,omitempty"`
	Tools             []json.RawMessage `json:"tools,omitempty"`
	Messages          []json.RawMessage `json:"messages"`
	Betas             []string          `json:"-"`
}

type Thinking struct {
	Type    string `json:"type"`
	Display string `json:"display,omitempty"`
}

type OutputConfig struct {
	Effort string `json:"effort"`
}

func (p Params) body() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
