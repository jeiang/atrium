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
		for _, c := range v.Credentials {
			fields = append(fields, ConnectionField{Name: c.Slot, Type: FieldSecret, Secret: true, HostOwned: strings.HasPrefix(c.Source, connections.SourceHost), Present: c.Present})
		}
		for _, slot := range unsetSlots(v, slots[v.Kind]) {
			fields = append(fields, ConnectionField{Name: slot, Type: FieldSecret, Secret: true})
		}
		out.Connections = append(out.Connections, ConnectionSchema{ID: v.ID, Kind: v.Kind, Label: v.Label, Fields: fields})
	}
	return application.Read[ConnectionsSchema]{Revision: read.Revision, Data: out}, nil
}

func (s *Service) UnsetSlots(v View) []string {
	for _, def := range s.store.Definitions() {
		if def.Kind == v.Kind {
			return unsetSlots(v, def.CredentialSlots)
		}
	}
	return nil
}

func unsetSlots(v View, patterns []string) []string {
	var out []string
	for _, pattern := range patterns {
		slot := pattern
		if strings.Contains(pattern, "*") {
			_, name, _ := strings.Cut(string(v.ID), "/")
			slot = strings.Replace(pattern, "*", name, 1)
			if name == "" || !strings.HasPrefix(slot, string(v.ID)+"/") {
				continue
			}
		}
		if !slices.ContainsFunc(v.Credentials, func(c connections.CredentialView) bool { return c.Slot == slot }) {
			out = append(out, slot)
		}
	}
	return out
}
