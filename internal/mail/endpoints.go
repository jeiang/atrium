package mail

import (
	"context"
	"encoding/json"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
)

func EmbeddingTestFailure(ctx context.Context, q db.Querier) (string, error) {
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
	e, err := endpoint.OpenEmbedding(ctx, q)
	if err != nil {
		if endpoint.IsUnconfigured(err) {
			return "", nil
		}
		return "", err
	}
	current := e.Fingerprint()
	e.Close()
	fingerprint := resultFingerprint(view.Data)
	if fingerprint == "" || fingerprint != current {
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
