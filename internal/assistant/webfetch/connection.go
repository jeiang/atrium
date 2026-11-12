package webfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/connections"
)

const KindWebfetch connections.Kind = "service/webfetch"

type connectionConfig struct {
	URL string `json:"url"`
}

func Connections() []connections.Definition {
	return []connections.Definition{{
		Kind:     KindWebfetch,
		Validate: validateConnection,
		Test:     testConnection,
	}}
}

func validateConnection(c connections.Candidate) error {
	var cfg connectionConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return fmt.Errorf("web fetch config: %w", err)
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return fmt.Errorf("web fetch needs a probe URL")
	}
	if _, err := NormalizeURL(cfg.URL); err != nil {
		return fmt.Errorf("web fetch probe URL %w", err)
	}
	return nil
}

func testConnection(ctx context.Context, lease *connections.Lease, _ []connections.Capability) ([]connections.Result, error) {
	target, err := lease.Endpoint("url")
	if err != nil {
		return nil, err
	}
	page, err := (&Fetcher{}).FetchPage(ctx, target, Limits{
		MaxBytes:     assistant.TuningAgentWebfetchMaxBytes,
		Timeout:      time.Duration(assistant.TuningAgentWebfetchTimeoutSeconds) * time.Second,
		MaxRedirects: assistant.TuningAgentWebfetchMaxRedirects,
		PageChars:    assistant.TuningAgentWebfetchPageChars,
		TextDefault:  assistant.TuningAgentWebfetchTextDefault,
		UserAgent:    assistant.TuningAgentWebfetchUserAgent,
	})
	if err != nil {
		return []connections.Result{{Capability: "egress", State: connections.Failing, Reason: err.Error()}}, nil
	}
	data, err := json.Marshal(map[string]int{"bytes": len(page.Body)})
	if err != nil {
		return nil, err
	}
	return []connections.Result{{Capability: "egress", State: connections.OK, Data: data}}, nil
}
