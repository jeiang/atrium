package mail

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
)

type Probe struct {
	Kind      string
	OK        int64
	ProbedAt  float64
	Dimension int64
}

func Probes(ctx context.Context, q db.Querier) ([]Probe, error) {
	rows, err := q.QueryContext(ctx, "SELECT kind, ok, probed_at, COALESCE(dimension, 0) FROM endpoint_probes ORDER BY kind")
	if err != nil {
		return nil, err
	}
	probes, err := db.Collect(rows, func(r *sql.Rows) (Probe, error) {
		var p Probe
		err := r.Scan(&p.Kind, &p.OK, &p.ProbedAt, &p.Dimension)
		return p, err
	})
	if err != nil {
		return nil, err
	}
	view, err := endpoint.EmbeddingState(ctx, q)
	if err != nil {
		if endpoint.IsUnconfigured(err) {
			return probes, nil
		}
		return nil, err
	}
	probes = append(probes, embeddingProbe(view))
	slices.SortFunc(probes, func(a, b Probe) int { return strings.Compare(a.Kind, b.Kind) })
	return probes, nil
}

func embeddingProbe(view endpoint.EmbeddingView) Probe {
	p := Probe{Kind: endpoint.EndpointEmbed}
	if view.State == "ok" {
		p.OK = 1
	}
	if view.CheckedAt != nil {
		p.ProbedAt = float64(view.CheckedAt.UnixNano()) / 1e9
	}
	var data struct {
		Dimension int `json:"dimension"`
	}
	if json.Unmarshal(view.Data, &data) == nil {
		p.Dimension = int64(data.Dimension)
	}
	return p
}
