package mail

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/llm"
)

type ProbeResult struct {
	Kind      string   `json:"kind"`
	URL       string   `json:"url"`
	Model     string   `json:"model"`
	OK        bool     `json:"ok"`
	Reason    string   `json:"reason"`
	Dimension int      `json:"dimension,omitempty"`
	Models    []string `json:"models"`
	Hosted    bool     `json:"hosted"`
}

func discover(ctx context.Context, base string) []string {
	text, err := httpx.GetText(ctx, base+"/models", 10*time.Second)
	if err != nil {
		return nil
	}
	var reply struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(text), &reply) != nil {
		return nil
	}
	ids := make([]string, 0, len(reply.Data))
	for _, m := range reply.Data {
		ids = append(ids, m.ID)
	}
	slices.Sort(ids)
	return ids
}

var probeSchema = map[string]any{
	"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
	"required": []string{"ok"}, "additionalProperties": false,
}

func probeChat(ctx context.Context, e endpoint.Endpoint) (string, int) {
	raw, err := llm.ChatJSON(endpoint.With(ctx, endpoint.Settings{ChatModel: e.Model, MaxTokens: endpoint.TuningMailChatMaxTokens}), e.URL, "Reply with JSON only.", `Return {"ok": true}.`, probeSchema, 0)
	if err != nil {
		return httpx.Message(err), 0
	}
	var answer struct {
		OK *bool `json:"ok"`
	}
	if json.Unmarshal(raw, &answer) != nil || answer.OK == nil {
		return "the reply did not follow the JSON schema", 0
	}
	return "", 0
}

func probe(ctx context.Context, e endpoint.Endpoint) ProbeResult {
	res := ProbeResult{Kind: e.Kind, URL: e.URL, Model: e.Model, Hosted: endpoint.Hosted(e.URL)}
	if e.Kind != endpoint.EndpointChat {
		res.Reason = "the embed endpoint is tested as a connection"
		return res
	}
	if e.URL == "" {
		res.Reason = "no URL configured"
	} else {
		res.Models = discover(ctx, e.URL)
		res.Reason, res.Dimension = probeChat(ctx, e)
	}
	res.OK = res.Reason == ""
	if res.Models == nil {
		res.Models = []string{}
	}
	return res
}

func recordProbe(ctx context.Context, d *db.DB, e endpoint.Endpoint, res ProbeResult, now float64) error {
	models, _ := json.Marshal(res.Models)
	_, err := d.Write.ExecContext(ctx, "INSERT INTO endpoint_probes (kind, fingerprint, ok, reason, dimension, models, probed_at) VALUES (?, ?, ?, ?, ?, ?, ?) "+
		"ON CONFLICT(kind) DO UPDATE SET fingerprint = excluded.fingerprint, ok = excluded.ok, reason = excluded.reason, "+
		"dimension = excluded.dimension, models = excluded.models, probed_at = excluded.probed_at",
		e.Kind, e.Fingerprint(), res.OK, res.Reason, nullDim(res.Dimension), string(models), now)
	return err
}

func ProbeEndpoints(ctx context.Context, d *db.DB, now float64) ([]ProbeResult, error) {
	endpoints, err := endpoint.Endpoints(ctx, d.Read)
	if err != nil {
		return nil, err
	}
	if ctx, err = endpoint.WithEndpoints(ctx, d.Read, endpoints); err != nil {
		return nil, err
	}
	out := make([]ProbeResult, 0, len(endpoints))
	for _, e := range endpoints {
		if e.Kind != endpoint.EndpointChat {
			res, err := probeEmbedding(ctx, d)
			if err != nil {
				return nil, err
			}
			out = append(out, res)
			continue
		}
		res := probe(ctx, e)
		if err := recordProbe(ctx, d, e, res, now); err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, nil
}

func probeEmbedding(ctx context.Context, d *db.DB) (ProbeResult, error) {
	view, err := endpoint.TestEmbedding(ctx, d)
	if err != nil {
		if endpoint.IsUnconfigured(err) {
			return ProbeResult{Kind: endpoint.EndpointEmbed, Model: endpoint.EmbedModel, Reason: "no embedding connection is configured", Models: []string{}}, nil
		}
		return ProbeResult{}, err
	}
	return probeResultFromView(view), nil
}

func probeResultFromView(view endpoint.EmbeddingView) ProbeResult {
	res := ProbeResult{Kind: endpoint.EndpointEmbed, OK: view.State == "ok", Reason: view.Reason, Models: []string{}}
	res.URL, res.Model = view.URL, view.Model
	if res.Model == "" {
		res.Model = endpoint.EmbedModel
	}
	var data struct {
		Dimension int      `json:"dimension"`
		Models    []string `json:"models"`
		Hosted    bool     `json:"hosted"`
	}
	_ = json.Unmarshal(view.Data, &data)
	res.Dimension, res.Hosted = data.Dimension, data.Hosted
	if data.Models != nil {
		res.Models = data.Models
	}
	return res
}

func ProbeCandidate(ctx context.Context, d *db.DB, kind, url, key string, client *http.Client, now float64) (ProbeResult, error) {
	if kind != endpoint.EndpointChat {
		return ProbeResult{}, errors.New("the embed endpoint is tested as a connection")
	}
	endpoints, err := endpoint.Endpoints(ctx, d.Read)
	if err != nil {
		return ProbeResult{}, err
	}
	e, ok := endpoint.Of(endpoints, kind)
	if !ok {
		return ProbeResult{}, errors.New("unknown endpoint kind " + kind)
	}
	e = endpoint.New(e.Kind, url, e.Model, key)
	res := probe(httpx.WithEndpoint(ctx, url, key, client), e)
	if res.OK {
		err = recordProbe(ctx, d, e, res, now)
	}
	return res, err
}

func nullDim(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func ProbeFailures(ctx context.Context, q db.Querier) (string, error) {
	endpoints, err := endpoint.Endpoints(ctx, q)
	if err != nil {
		return "", err
	}
	var reasons []string
	for _, e := range endpoints {
		if e.Kind != endpoint.EndpointChat {
			reason, err := embeddingFailure(ctx, q, e)
			if err != nil {
				return "", err
			}
			if reason != "" {
				reasons = append(reasons, reason)
			}
			continue
		}
		var fingerprint, reason string
		var ok bool
		err := q.QueryRowContext(ctx, "SELECT fingerprint, ok, reason FROM endpoint_probes WHERE kind = ?", e.Kind).Scan(&fingerprint, &ok, &reason)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", err
		}
		if fingerprint == e.Fingerprint() && !ok {
			reasons = append(reasons, e.Kind+" endpoint probe failed: "+reason)
		}
	}
	return strings.Join(reasons, "; "), nil
}

func embeddingFailure(ctx context.Context, q db.Querier, e endpoint.Endpoint) (string, error) {
	view, err := endpoint.EmbeddingState(ctx, q)
	if err != nil {
		if endpoint.IsUnconfigured(err) {
			return "", nil
		}
		return "", err
	}
	if !view.TestFailed {
		return "", nil
	}
	fingerprint := resultFingerprint(view.Data)
	if fingerprint == "" || fingerprint != e.Fingerprint() {
		return "", nil
	}
	reason := view.Reason
	if view.State != "failing" && view.State != "needs_reauth" {
		reason = "the explicit Test failed"
	}
	return "embed endpoint probe failed: " + reason, nil
}

func resultFingerprint(data json.RawMessage) string {
	var probe struct {
		Fingerprint string `json:"fingerprint"`
	}
	if json.Unmarshal(data, &probe) == nil {
		return probe.Fingerprint
	}
	return ""
}
