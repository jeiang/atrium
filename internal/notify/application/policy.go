package application

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/actionlink"
	"github.com/jeiang/atrium/internal/actionlink/policy"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/settings"
)

type KindRow struct {
	Name    string `json:"name"`
	On      bool   `json:"on"`
	WebOnly bool   `json:"web_only"`
}
type PhoneConfiguration struct {
	Enabled     bool      `json:"enabled"`
	Kinds       []KindRow `json:"kinds"`
	Outstanding int       `json:"outstanding"`
}
type Configuration struct {
	Channels []Channel          `json:"channels"`
	Error    string             `json:"error"`
	Push     PushConfiguration  `json:"push"`
	Phone    PhoneConfiguration `json:"phone"`
}
type PushConfiguration struct {
	Key     string       `json:"key"`
	Problem string       `json:"problem"`
	Devices []PushDevice `json:"devices"`
}
type PhonePolicyInput struct {
	Enabled bool
	Kinds   []string
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
	pol, err := policy.Load(ctx, s.db.Read)
	if err != nil {
		return application.Read[Configuration]{}, err
	}
	data.Phone.Enabled = pol.Enabled
	if data.Phone.Outstanding, err = actionlink.Outstanding(ctx, s.db.Read, stamp()); err != nil {
		return application.Read[Configuration]{}, err
	}
	names := map[string]bool{}
	for _, name := range policy.DefaultKinds {
		names[name] = true
	}
	for name := range pol.Kinds {
		names[name] = true
	}
	for name := range policy.WebOnly {
		names[name] = true
	}
	for name := range names {
		data.Phone.Kinds = append(data.Phone.Kinds, KindRow{Name: name, On: pol.Kinds[name], WebOnly: policy.WebOnly[name]})
	}
	sort.Slice(data.Phone.Kinds, func(i, j int) bool { return data.Phone.Kinds[i].Name < data.Phone.Kinds[j].Name })
	return application.Read[Configuration]{Data: data, Revision: application.Revision(state.Revision(string(ConfigurationName), counters, data, "operator"))}, nil
}
func (s *Service) PhonePolicy(ctx context.Context, in PhonePolicyInput) (application.Change[Empty], error) {
	var kinds []string
	for _, kind := range in.Kinds {
		if !policy.WebOnly[kind] {
			kinds = append(kinds, kind)
		}
	}
	enabled := "false"
	if in.Enabled {
		enabled = "true"
	}
	id, err := s.mutate(ctx, PhonePolicyName, "phone-policy", func(tx *sql.Tx) error {
		if err := settings.From(ctx).Set(ctx, tx, policy.EnabledKey, enabled); err != nil {
			return err
		}
		return settings.From(ctx).Set(ctx, tx, policy.KindsKey, strings.Join(kinds, ","))
	})
	return application.Change[Empty]{AuditID: id}, err
}
func (s *Service) RevokeLinks(ctx context.Context, _ Empty) (application.Change[Empty], error) {
	id, err := s.mutate(ctx, RevokeLinksName, "phone-links", func(tx *sql.Tx) error { return actionlink.Rotate(ctx, tx) })
	return application.Change[Empty]{AuditID: id}, err
}
