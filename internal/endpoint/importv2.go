package endpoint

import (
	"encoding/json"

	"github.com/jeiang/atrium/internal/connections"
)

type ImportProbe struct {
	Fingerprint string
	OK          bool
	Reason      string
	Dimension   int
	Models      []string
	ProbedAt    float64
}

func ImportV2(values map[string]string, probe *ImportProbe, health *connections.ImportHealth, ambiguous bool, key connections.ImportCredential) (connections.ImportService, []string) {
	service := connections.ImportService{
		ID:           EmbeddingID,
		Kind:         KindEmbedding,
		Label:        "Embedding",
		Source:       connections.SourceHost,
		Config:       map[string]string{"url": values["mail_embed_url"], "model": values["mail_embed_model"]},
		Transport:    map[string]string{"ca_file": values["mail_embed_ca_file"], "proxy": values["mail_embed_proxy"]},
		Capabilities: []string{"embeddings"},
		Credentials:  []connections.ImportCredential{key},
	}
	var notes []string
	var probeState connections.State
	probeCarried := false
	if probe != nil {
		probeState = connections.Failing
		if probe.OK {
			probeState = connections.OK
		}
		data, err := json.Marshal(map[string]any{
			"dimension":   probe.Dimension,
			"models":      probe.Models,
			"fingerprint": probe.Fingerprint,
			"probed_at":   probe.ProbedAt,
			"legacy":      true,
		})
		if err == nil {
			service.Results = append(service.Results, connections.Result{Capability: "embeddings", State: probeState, Reason: probe.Reason, Data: data})
			service.Health = &connections.ImportHealth{State: string(probeState), Reason: probe.Reason, CheckedAt: probe.ProbedAt}
			probeCarried = true
		}
	}
	if health == nil {
		return service, notes
	}
	healthState := connections.State(health.State)
	if ambiguous {
		data, _ := json.Marshal(map[string]any{"legacy": true, "checked_at": health.CheckedAt, "ambiguous": "mail_embed"})
		service.Results = append(service.Results, connections.Result{
			Capability: "embeddings", State: connections.Untested,
			Reason: "ambiguous-health mail_embed: " + health.Reason, Data: data,
		})
		notes = append(notes, "ambiguous-health mail_embed: the name is both the embedding endpoint and a mail account; the account is untested")
		return service, notes
	}
	switch {
	case healthState == connections.Failing || healthState == connections.NeedsReauth || healthState == connections.Expiring:
		service.Health = &connections.ImportHealth{State: health.State, Reason: health.Reason, CheckedAt: health.CheckedAt, ExpiresAt: health.ExpiresAt}
	case probe == nil:
		service.Health = &connections.ImportHealth{State: health.State, Reason: health.Reason, CheckedAt: health.CheckedAt, ExpiresAt: health.ExpiresAt}
	}
	if probeCarried {
		if data, err := json.Marshal(map[string]any{
			"dimension":   probe.Dimension,
			"models":      probe.Models,
			"fingerprint": probe.Fingerprint,
			"probed_at":   probe.ProbedAt,
			"legacy":      true,
			"checked_at":  health.CheckedAt,
		}); err == nil {
			service.Results[len(service.Results)-1] = connections.Result{
				Capability: "embeddings", State: connections.State(service.Health.State), Reason: service.Health.Reason, Data: data,
			}
		}
		return service, notes
	}
	data, _ := json.Marshal(map[string]any{"legacy": true, "checked_at": health.CheckedAt})
	service.Results = append(service.Results, connections.Result{Capability: "embeddings", State: healthState, Reason: health.Reason, Data: data})
	return service, notes
}
