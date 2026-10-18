package anthropic_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const slice = "anthropic"

var goldenHeaders = []string{"anthropic-beta", "anthropic-version", "content-type"}

func paramsOf(t *testing.T, req golden.Request) (anthropic.Params, bool) {
	t.Helper()
	var p anthropic.Params
	if err := json.Unmarshal(req.Body, &p); err != nil {
		t.Fatalf("golden body: %v", err)
	}
	if betas := req.Headers["anthropic-beta"]; betas != "" {
		p.Betas = strings.Split(betas, ",")
	}
	var head struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(req.Body, &head)
	return p, head.Stream
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGoldenRequests(t *testing.T) {
	manifest := golden.LoadManifest(t, slice)
	if len(manifest.Scenarios) == 0 {
		t.Fatal("no scenarios in the manifest")
	}
	for _, entry := range manifest.Scenarios {
		t.Run(entry.Name, func(t *testing.T) {
			scenario := golden.LoadScenario(t, slice, entry.Name)
			if len(scenario.Exchanges) != entry.Requests {
				t.Fatalf("%d exchanges, manifest says %d", len(scenario.Exchanges), entry.Requests)
			}
			srv := anthropictest.New(t)
			for _, ex := range scenario.Exchanges {
				resp, err := anthropictest.FromGolden(ex.Response)
				if err != nil {
					t.Fatalf("golden response: %v", err)
				}
				srv.Push(resp)
			}
			client := srv.Client()
			for i, ex := range scenario.Exchanges {
				p, streamed := paramsOf(t, ex.Request)
				var msg *anthropic.Message
				var err error
				if streamed {
					msg, err = client.Stream(context.Background(), p, nil)
				} else {
					msg, err = client.Create(context.Background(), p)
				}
				if err != nil {
					t.Fatalf("request %d: %v", i, err)
				}
				sent := srv.Requests()[i]
				checkRequest(t, i, ex.Request, sent)
				checkMessage(t, i, ex.Response, msg)
			}
			if n := len(srv.Requests()); n != len(scenario.Exchanges) {
				t.Errorf("%d requests sent, golden has %d", n, len(scenario.Exchanges))
			}
		})
	}
}

func checkRequest(t *testing.T, i int, want golden.Request, got anthropictest.Request) {
	t.Helper()
	if got.Method != want.Method || got.Path != want.Path || got.Query != want.Query {
		t.Errorf("request %d: %s %s?%s, golden %s %s?%s", i, got.Method, got.Path, got.Query, want.Method, want.Path, want.Query)
	}
	for _, h := range goldenHeaders {
		if g, w := strings.Join(got.Header.Values(h), ","), want.Headers[h]; g != w {
			t.Errorf("request %d: header %s = %q, golden %q", i, h, g, w)
		}
	}
	golden.EqualJSON(t, want.Body, got.Body)
}

func checkMessage(t *testing.T, i int, raw json.RawMessage, msg *anthropic.Message) {
	t.Helper()
	var want struct {
		Content           json.RawMessage `json:"content"`
		StopReason        string          `json:"stop_reason"`
		Usage             json.RawMessage `json:"usage"`
		ContextManagement json.RawMessage `json:"context_management"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if msg.StopReason != want.StopReason {
		t.Errorf("response %d: stop_reason %q, golden %q", i, msg.StopReason, want.StopReason)
	}
	golden.EqualJSON(t, want.Content, mustMarshal(t, msg.Content))
	golden.EqualJSON(t, want.Usage, mustMarshal(t, msg.Usage))
	golden.EqualJSON(t, want.ContextManagement, mustMarshal(t, msg.ContextManagement))
}
