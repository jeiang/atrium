package anthropic

import (
	"encoding/json"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

// Event kinds carry the progress journal's names (agent/progress.py).
const (
	EventThinking  = "thinking"
	EventText      = "text"
	EventToolStart = "tool_start"
	EventToolDone  = "tool_done"
)

const (
	blockServerToolUse   = "server_tool_use"
	blockWebSearchResult = "web_search_tool_result"
)

// Event is what progress.Writer.observe made of one stream event: a thinking or text delta (Text), a server
// tool starting (ID, Name, Input: the value short_args formats) or its result arriving (ID, OK, Preview).
type Event struct {
	Kind    string
	Text    string
	ID      string
	Name    string
	Input   json.RawMessage
	OK      bool
	Preview string
}

type stoppedBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

func eventOf(acc *sdk.BetaMessage, ev sdk.BetaRawMessageStreamEventUnion) (Event, error) {
	switch ev.Type {
	case "content_block_delta":
		switch ev.Delta.Type {
		case "thinking_delta":
			return Event{Kind: EventThinking, Text: ev.Delta.Thinking}, nil
		case "text_delta":
			return Event{Kind: EventText, Text: ev.Delta.Text}, nil
		}
	case "content_block_stop":
		var b stoppedBlock
		if err := json.Unmarshal([]byte(acc.Content[ev.Index].RawJSON()), &b); err != nil {
			return Event{}, fmt.Errorf("anthropic: decoding content block %d: %w", ev.Index, err)
		}
		switch b.Type {
		case blockServerToolUse:
			return Event{Kind: EventToolStart, ID: b.ID, Name: b.Name, Input: b.Input}, nil
		case blockWebSearchResult:
			var results []json.RawMessage
			ok := json.Unmarshal(b.Content, &results) == nil && results != nil
			return Event{Kind: EventToolDone, ID: b.ToolUseID, OK: ok, Preview: fmt.Sprintf("%d results", len(results))}, nil
		}
	}
	return Event{}, nil
}
