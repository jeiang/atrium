package loop

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/anthropic"
	actx "github.com/jeiang/atrium/internal/assistant/context"
)

const (
	DoomLoopThreshold = 3

	stopRefusal       = "refusal"
	stopContextWindow = "model_context_window_exceeded"
)

func textBlock(text string) json.RawMessage {
	raw, err := json.Marshal(map[string]string{"type": "text", "text": text})
	if err != nil {
		panic(err)
	}
	return raw
}

func hasVisibleText(blocks []json.RawMessage) bool {
	for _, raw := range blocks {
		var b struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &b) == nil && b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return true
		}
	}
	return false
}

func withoutToolUses(blocks []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(blocks))
	for _, raw := range blocks {
		if blockType(raw) != actx.ToolUse {
			out = append(out, raw)
		}
	}
	return out
}

func judge(stopReason string, blocks []json.RawMessage) (out []json.RawMessage, stop error) {
	switch stopReason {
	case stopRefusal:
		return []json.RawMessage{textBlock("The model declined this request (stop reason: refusal). Nothing it produced was kept. Rephrase the request or start a new thread.")},
			RunLimit("the model refused the request (stop reason: refusal)")
	case anthropic.StopMaxTokens, stopContextWindow:
		kept := withoutToolUses(blocks)
		note := "This reply was cut off at the output token limit (stop reason: max_tokens). Any tool call it was writing was dropped."
		if stopReason == stopContextWindow {
			note = "This reply was cut off because the context window filled (stop reason: model_context_window_exceeded). Any tool call it was writing was dropped."
		}
		return append(kept, textBlock(note)), RunLimit(fmt.Sprintf("the reply was cut off (stop reason: %s)", stopReason))
	case anthropic.StopPauseTurn:
		if len(blocks) == 0 {
			return []json.RawMessage{textBlock("The model paused its turn without producing anything (stop reason: pause_turn).")},
				RunLimit("the model paused its turn with no content (stop reason: pause_turn)")
		}
	case anthropic.StopEndTurn:
		if !hasVisibleText(blocks) && len(withoutToolUses(blocks)) == len(blocks) {
			return append(blocks, textBlock("The model ended its turn without a reply.")), nil
		}
	}
	return blocks, nil
}

func callsKey(uses []ToolUse, results []json.RawMessage) (string, error) {
	var b strings.Builder
	for _, u := range uses {
		var input any
		if len(bytes.TrimSpace(u.Input)) > 0 {
			if err := json.Unmarshal(u.Input, &input); err != nil {
				return "", fmt.Errorf("loop: tool %s input: %w", u.Name, err)
			}
		}
		canon, err := json.Marshal(input)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "call %s %s\n", u.Name, canon)
	}
	for _, raw := range results {
		var r struct {
			Content json.RawMessage `json:"content"`
			IsError bool            `json:"is_error"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return "", fmt.Errorf("loop: tool result: %w", err)
		}
		fmt.Fprintf(&b, "result %t %s\n", r.IsError, r.Content)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return string(sum[:]), nil
}

func (s *state) repeat(uses []ToolUse, results []json.RawMessage) error {
	key, err := callsKey(uses, results)
	if err != nil {
		return err
	}
	if key == s.callsKey {
		s.repeats++
	} else {
		s.callsKey, s.repeats = key, 1
	}
	if s.repeats >= DoomLoopThreshold {
		names := make([]string, len(uses))
		for i, u := range uses {
			names[i] = u.Name
		}
		return RunLimit(fmt.Sprintf("stopped: the same tool call (%s) repeated %d times with the same result", strings.Join(names, ", "), s.repeats))
	}
	return nil
}
