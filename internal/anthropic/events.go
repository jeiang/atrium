package anthropic

import (
	"encoding/json"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

type Event interface{ event() }

type ThinkingEvent struct{ Text string }

type TextEvent struct{ Text string }

type ToolStartEvent struct {
	ID    string
	Name  string
	Input json.RawMessage
}

type ToolDoneEvent struct {
	ID      string
	OK      bool
	Preview string
}

func (ThinkingEvent) event()  {}
func (TextEvent) event()      {}
func (ToolStartEvent) event() {}
func (ToolDoneEvent) event()  {}

func eventOf(acc *sdk.BetaMessage, ev sdk.BetaRawMessageStreamEventUnion) Event {
	switch ev.Type {
	case "content_block_delta":
		switch ev.Delta.Type {
		case "thinking_delta":
			return ThinkingEvent{Text: ev.Delta.Thinking}
		case "text_delta":
			return TextEvent{Text: ev.Delta.Text}
		}
	case "content_block_stop":
		block := acc.Content[ev.Index]
		switch block.Type {
		case "server_tool_use":
			return ToolStartEvent{ID: block.ID, Name: block.Name, Input: block.Input}
		case "web_search_tool_result":
			results := block.Content.OfBetaWebSearchResultBlockArray
			return ToolDoneEvent{ID: block.ToolUseID, OK: block.Content.ErrorCode == "", Preview: fmt.Sprintf("%d results", len(results))}
		}
	}
	return nil
}
