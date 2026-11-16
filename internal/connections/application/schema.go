package application

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/connections"
)

const (
	FieldText   = "text"
	FieldURL    = "url"
	FieldSecret = "secret"
)

type ConnectionsSchema struct {
	Kinds       []ConnectionKindSchema `json:"kinds"`
	Connections []ConnectionSchema     `json:"connections"`
}

type ConnectionKindSchema struct {
	Kind   connections.Kind  `json:"kind"`
	Fields []ConnectionField `json:"fields"`
}

type ConnectionSchema struct {
	ID     ID                `json:"id"`
	Kind   connections.Kind  `json:"kind"`
	Label  string            `json:"label"`
	Fields []ConnectionField `json:"fields"`
}

type ConnectionField struct {
	Name      string `json:"name"`
	Type      string `json:"type" api:"enum=text|url|secret"`
	Secret    bool   `json:"secret"`
	HostOwned bool   `json:"host_owned"`
	Present   bool   `json:"present"`
}

func (s *Service) Schema(ctx context.Context, in ListInput) (application.Read[ConnectionsSchema], error) {
	read, err := s.List(ctx, in)
	if err != nil {
		return application.Read[ConnectionsSchema]{}, err
	}
	out := ConnectionsSchema{Kinds: []ConnectionKindSchema{}, Connections: []ConnectionSchema{}}
	slots := map[connections.Kind][]string{}
	for _, def := range s.store.Definitions() {
		fields := []ConnectionField{{Name: "label", Type: FieldText}}
		for _, slot := range def.CredentialSlots {
			fields = append(fields, ConnectionField{Name: slot, Type: FieldSecret, Secret: true})
		}
		slots[def.Kind] = def.CredentialSlots
		out.Kinds = append(out.Kinds, ConnectionKindSchema{Kind: def.Kind, Fields: fields})
	}
	for _, v := range read.Data {
		host := v.ConfigSource == connections.SourceHost
		fields := []ConnectionField{{Name: "label", Type: FieldText, HostOwned: host, Present: v.Label != ""}}
		var config map[string]any
		_ = json.Unmarshal(v.Config, &config)
		if url, ok := config["url"].(string); ok {
			fields = append(fields, ConnectionField{Name: "url", Type: FieldURL, HostOwned: host, Present: url != ""})
		}
		stored := make([]string, 0, len(v.Credentials))
		for _, c := range v.Credentials {
			stored = append(stored, c.Slot)
			fields = append(fields, ConnectionField{Name: c.Slot, Type: FieldSecret, Secret: true, HostOwned: strings.HasPrefix(c.Source, connections.SourceHost), Present: c.Present})
		}
		for _, slot := range slots[v.Kind] {
			if !strings.Contains(slot, "*") && !slices.Contains(stored, slot) {
				fields = append(fields, ConnectionField{Name: slot, Type: FieldSecret, Secret: true})
			}
		}
		out.Connections = append(out.Connections, ConnectionSchema{ID: v.ID, Kind: v.Kind, Label: v.Label, Fields: fields})
	}
	return application.Read[ConnectionsSchema]{Revision: read.Revision, Data: out}, nil
}
