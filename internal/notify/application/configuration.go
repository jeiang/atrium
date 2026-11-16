package application

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/notify"
)

type Configuration struct {
	Channels []Channel         `json:"channels"`
	Error    string            `json:"error"`
	Push     PushConfiguration `json:"push"`
}
type PushConfiguration struct {
	Key     string       `json:"key"`
	Problem string       `json:"problem"`
	Devices []PushDevice `json:"devices"`
}

func stamp() float64 { return float64(time.Now().UnixNano()) / 1e9 }
func (s *Service) Configuration(ctx context.Context, _ Empty) (application.Read[Configuration], error) {
	counters, err := s.state.Read(ctx, nil, []string{state.Notify, state.Settings, state.Connections})
	if err != nil {
		return application.Read[Configuration]{}, err
	}
	var data Configuration
	if data.Channels, err = notify.ListChannels(ctx, s.db.Read); err != nil {
		return application.Read[Configuration]{}, err
	}
	if data.Push.Devices, err = notify.Devices(ctx, s.db.Read); err != nil {
		return application.Read[Configuration]{}, err
	}
	if data.Push.Key, err = notify.PushPublicKey(ctx, s.db); err != nil {
		data.Push.Problem = err.Error()
	}
	return application.Read[Configuration]{Data: data, Revision: application.Revision(state.Revision(string(ConfigurationName), counters, data, "operator"))}, nil
}
