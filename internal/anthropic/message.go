package anthropic

import (
	"encoding/json"
	"fmt"
)

type Message struct {
	ID                string             `json:"id"`
	Model             string             `json:"model"`
	Content           []json.RawMessage  `json:"content"`
	StopReason        string             `json:"stop_reason"`
	Usage             Usage              `json:"usage"`
	ContextManagement *ContextManagement `json:"context_management"`
}

type Usage struct {
	InputTokens              int64          `json:"input_tokens"`
	CacheCreationInputTokens int64          `json:"cache_creation_input_tokens"`
	CacheCreation            *CacheCreation `json:"cache_creation"`
	CacheReadInputTokens     int64          `json:"cache_read_input_tokens"`
	OutputTokens             int64          `json:"output_tokens"`
	ServerToolUse            *ServerToolUse `json:"server_tool_use"`
	Iterations               []Iteration    `json:"iterations"`
}

type CacheCreation struct {
	Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
}

type ServerToolUse struct {
	WebSearchRequests int64 `json:"web_search_requests"`
	WebFetchRequests  int64 `json:"web_fetch_requests"`
}

type Iteration struct {
	Type                     string         `json:"type"`
	InputTokens              int64          `json:"input_tokens"`
	CacheCreationInputTokens int64          `json:"cache_creation_input_tokens"`
	CacheCreation            *CacheCreation `json:"cache_creation"`
	CacheReadInputTokens     int64          `json:"cache_read_input_tokens"`
	OutputTokens             int64          `json:"output_tokens"`
}

type ContextManagement struct {
	AppliedEdits []AppliedEdit `json:"applied_edits"`
}

type AppliedEdit struct {
	Type               string `json:"type"`
	ClearedInputTokens int64  `json:"cleared_input_tokens"`
	ClearedToolUses    int64  `json:"cleared_tool_uses"`
}

func decodeMessage(raw string) (*Message, error) {
	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("anthropic: decoding message: %w", err)
	}
	return &m, nil
}
