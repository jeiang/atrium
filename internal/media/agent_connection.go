package media

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/media/agentclient"
	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

func validateMediaAgent(c connections.Candidate) error {
	var cfg struct {
		URL string `json:"url"`
	}
	if err := protocol.Decode(strings.NewReader(string(c.Config)), 1<<20, &cfg); err != nil {
		return errors.New("the media agent config requires only url")
	}
	return agentclient.ValidateURL(cfg.URL)
}

func testMediaAgent(ctx context.Context, lease *connections.Lease, caps []connections.Capability) ([]connections.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client, err := agentclient.New(lease)
	var health protocol.Health
	if err == nil {
		health, err = client.Health(ctx)
	}
	state, reason := connections.OK, ""
	var data json.RawMessage
	if err != nil {
		state, reason = connections.Failing, "unavailable"
		var e *agentclient.Error
		if errors.As(err, &e) && (e.Status == 401 || e.Status == 403) {
			state, reason = connections.NeedsReauth, "bearer refused"
		}
	} else if health.Protocol != protocol.Version {
		state, reason = connections.Failing, "protocol mismatch"
	} else if !health.Ready {
		state, reason = connections.Failing, "not ready"
	} else {
		data, _ = json.Marshal(map[string]string{"agent_id": health.AgentID, "root_revision": health.RootRevision})
	}
	out := make([]connections.Result, 0, len(caps))
	for _, cap := range caps {
		if !cap.Enabled {
			continue
		}
		result := connections.Result{Capability: cap.Name, State: state, Reason: reason, Data: data}
		if state == connections.OK && !slices.Contains(health.Capabilities, cap.Name) {
			result.State, result.Reason, result.Data = connections.Failing, "capability unavailable", nil
		}
		out = append(out, result)
	}
	return out, nil
}

func (s Settings) Agent() (*agentclient.Client, error) {
	if s.AgentLease == nil {
		return nil, config.NotConfigured("the media agent connection is not configured")
	}
	client, err := agentclient.New(s.AgentLease)
	if err != nil {
		return nil, err
	}
	if s.QbitStore != nil {
		lease, store := s.AgentLease, s.QbitStore
		client.Observe = func(ctx context.Context, state connections.State, reason string) {
			_ = store.Observe(context.WithoutCancel(ctx), lease.ID(), lease.Revision(), connections.Result{Capability: "file", State: state, Reason: reason})
		}
	}
	return client, nil
}
