package anthropictest

import (
	"encoding/json"
	"fmt"
)

type Usage struct {
	Input         int64
	CacheCreation int64
	CacheRead     int64
	Output        int64
	Searches      int64
	Cache1h       int64
	Iterations    []Iteration
}

var Default = Usage{Input: 1000, Output: 100}

type Iteration struct {
	Type          string
	Model         string
	Input         int64
	CacheCreation int64
	CacheRead     int64
	Output        int64
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("anthropictest: %v", err))
	}
	return data
}

func (u Usage) JSON() json.RawMessage {
	var iterations any
	if u.Iterations != nil {
		list := make([]map[string]any, len(u.Iterations))
		for i, it := range u.Iterations {
			list[i] = map[string]any{
				"type":                        it.Type,
				"input_tokens":                it.Input,
				"cache_creation_input_tokens": it.CacheCreation,
				"cache_creation":              map[string]int64{"ephemeral_5m_input_tokens": it.CacheCreation, "ephemeral_1h_input_tokens": 0},
				"cache_read_input_tokens":     it.CacheRead,
				"output_tokens":               it.Output,
			}
			if it.Model != "" {
				list[i]["model"] = it.Model
			}
		}
		iterations = list
	}
	return mustJSON(map[string]any{
		"input_tokens":                u.Input,
		"cache_creation_input_tokens": u.CacheCreation,
		"cache_creation":              map[string]int64{"ephemeral_5m_input_tokens": u.CacheCreation - u.Cache1h, "ephemeral_1h_input_tokens": u.Cache1h},
		"cache_read_input_tokens":     u.CacheRead,
		"output_tokens":               u.Output,
		"server_tool_use":             map[string]int64{"web_search_requests": u.Searches, "web_fetch_requests": 0},
		"iterations":                  iterations,
	})
}

func Reply(stop string, content ...json.RawMessage) Response {
	return Response{Content: content, StopReason: stop, Usage: Default.JSON()}
}

func (r Response) WithUsage(u Usage) Response {
	r.Usage = u.JSON()
	return r
}

func (r Response) WithContextManagement(cm json.RawMessage) Response {
	r.ContextManagement = cm
	return r
}

func FromGolden(raw json.RawMessage) (Response, error) {
	var r Response
	err := json.Unmarshal(raw, &r)
	return r, err
}

func Failure(status int, errType, message string) Response {
	return Response{Status: status, ErrorBody: ErrorBody(errType, message)}
}

func StreamFailure(errType, message string) Response {
	return Response{ErrorBody: ErrorBody(errType, message)}
}

func ErrorBody(errType, message string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":"error","error":{"type":%s,"message":%s}}`, mustJSON(errType), mustJSON(message)))
}

func Refusal(category, explanation string, content ...json.RawMessage) Response {
	r := Reply("refusal", content...)
	r.StopDetails = mustJSON(map[string]any{"type": "refusal", "category": category, "explanation": explanation})
	return r
}

func Fallback(from, to, category string) json.RawMessage {
	return mustJSON(map[string]any{
		"type": "fallback", "from": map[string]string{"model": from}, "to": map[string]string{"model": to},
		"trigger": map[string]string{"type": "refusal", "category": category},
	})
}

func Cleared(clearedInputTokens, clearedToolUses int64) json.RawMessage {
	return mustJSON(map[string]any{"applied_edits": []map[string]any{{
		"type": "clear_tool_uses_20250919", "cleared_input_tokens": clearedInputTokens, "cleared_tool_uses": clearedToolUses,
	}}})
}

func Text(value string, citations ...json.RawMessage) json.RawMessage {
	block := map[string]any{"type": "text", "text": value}
	if len(citations) > 0 {
		block["citations"] = citations
	}
	return mustJSON(block)
}

func Thinking(value, signature string) json.RawMessage {
	return mustJSON(map[string]string{"type": "thinking", "thinking": value, "signature": signature})
}

func ToolUse(id, name string, input map[string]any) json.RawMessage {
	if input == nil {
		input = map[string]any{}
	}
	return mustJSON(map[string]any{"type": "tool_use", "id": id, "name": name, "input": input})
}

func Compaction(summary, signature string) json.RawMessage {
	return mustJSON(map[string]string{"type": "compaction", "content": summary, "encrypted_content": "enc", "signature": signature})
}

func WebSearch(id, query string, urls ...string) []json.RawMessage {
	results := make([]map[string]any, len(urls))
	for i, u := range urls {
		results[i] = map[string]any{"type": "web_search_result", "url": u, "title": fmt.Sprintf("Page %d", i), "encrypted_content": "x", "page_age": nil}
	}
	return []json.RawMessage{
		mustJSON(map[string]any{"type": "server_tool_use", "id": id, "name": "web_search", "input": map[string]string{"query": query}}),
		mustJSON(map[string]any{"type": "web_search_tool_result", "tool_use_id": id, "content": results}),
	}
}
