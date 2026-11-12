package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
)

type chatRequest struct {
	Model              string        `json:"model"`
	Messages           []chatMessage `json:"messages"`
	Temperature        int           `json:"temperature"`
	MaxTokens          int           `json:"max_tokens"`
	CachePrompt        bool          `json:"cache_prompt"`
	ChatTemplateKwargs struct {
		EnableThinking bool `json:"enable_thinking"`
	} `json:"chat_template_kwargs"`
	ResponseFormat struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Name   string `json:"name"`
			Strict bool   `json:"strict"`
			Schema any    `json:"schema"`
		} `json:"json_schema"`
	} `json:"response_format"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func ChatJSON(ctx context.Context, url, system, user string, schema any, maxTokens int) (json.RawMessage, error) {
	s := endpoint.From(ctx)
	if maxTokens <= 0 {
		maxTokens = s.MaxTokens
	}
	req := chatRequest{
		Model:       s.ChatModel,
		Messages:    []chatMessage{{"system", system}, {"user", user}},
		MaxTokens:   maxTokens,
		CachePrompt: true,
	}
	req.ResponseFormat.Type = "json_schema"
	req.ResponseFormat.JSONSchema.Name = "answer"
	req.ResponseFormat.JSONSchema.Strict = true
	req.ResponseFormat.JSONSchema.Schema = schema
	raw, err := httpx.PostJSON(ctx, url+"/chat/completions", req)
	if err != nil {
		return nil, err
	}
	var reply any
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("%w: %w", httpx.ErrPayload, err)
	}
	choices, err := httpx.Key(reply, "choices")
	if err != nil {
		return nil, err
	}
	first, err := httpx.Index(choices, 0)
	if err != nil {
		return nil, err
	}
	message, err := httpx.Key(first, "message")
	if err != nil {
		return nil, err
	}
	content, err := httpx.Key(message, "content")
	if err != nil {
		return nil, err
	}
	text, ok := content.(string)
	if !ok {
		return nil, fmt.Errorf("%w: chat reply content is not a string", httpx.ErrPayload)
	}
	if !json.Valid([]byte(text)) {
		return nil, fmt.Errorf("%w: chat reply content is not JSON: %q", httpx.ErrPayload, text)
	}
	return json.RawMessage(text), nil
}
