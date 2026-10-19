package subagents_test

import "encoding/json"

func mustText(text string) []byte {
	b, _ := json.Marshal(map[string]string{"type": "text", "text": text})
	return b
}

func rawBlocks(blocks [][]byte) []json.RawMessage {
	out := make([]json.RawMessage, len(blocks))
	for i, b := range blocks {
		out[i] = b
	}
	return out
}
