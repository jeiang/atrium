package connections

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

const qbittorrentID = "service/qbittorrent"

type ImportHealth struct {
	State     string
	Reason    string
	CheckedAt float64
	ExpiresAt *float64
}

type ImportQbit struct {
	URL    string
	CAFile string
	Proxy  string
	Key    string
	SetAt  float64
	SetBy  string
	Health *ImportHealth
}

type ImportCredential struct {
	Slot  string
	Value string
	SetAt float64
	SetBy string
}

type ImportService struct {
	ID           string
	Kind         string
	Label        string
	Config       map[string]string
	Transport    map[string]string
	Capabilities []string
	Credentials  []ImportCredential
	Health       *ImportHealth
}

func ImportV2(ctx context.Context, tx *sql.Tx, q ImportQbit, services []ImportService, encrypted bool, now float64) ([]string, error) {
	var lines []string
	if q.URL == "" {
		lines = append(lines, "connection "+qbittorrentID+": no host URL in the source; not materialized")
	} else {
		carried, err := importService(ctx, tx, ImportService{
			ID:           qbittorrentID,
			Label:        "qBittorrent",
			Config:       map[string]string{"url": q.URL},
			Transport:    map[string]string{"ca_file": q.CAFile, "proxy": q.Proxy},
			Capabilities: []string{"api"},
			Credentials:  []ImportCredential{{Slot: "api_key", Value: q.Key, SetAt: q.SetAt, SetBy: q.SetBy}},
			Health:       q.Health,
		}, encrypted, now)
		if err != nil {
			return nil, err
		}
		lines = append(lines, carried...)
	}
	for _, service := range services {
		carried, err := importService(ctx, tx, service, encrypted, now)
		if err != nil {
			return nil, err
		}
		lines = append(lines, carried...)
	}
	return lines, nil
}

func importService(ctx context.Context, tx *sql.Tx, s ImportService, encrypted bool, now float64) ([]string, error) {
	kind := s.Kind
	if kind == "" {
		kind = s.ID
	}
	configJSON, err := json.Marshal(compact(s.Config))
	if err != nil {
		return nil, err
	}
	transportJSON, err := json.Marshal(compact(s.Transport))
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO connections (id, kind, label, config_source, config_json, transport_json, revision, enabled, state, created_at, updated_at) "+
			"VALUES (?, ?, ?, 'host', ?, ?, 1, 1, 'untested', ?, ?)",
		s.ID, kind, s.Label, string(configJSON), string(transportJSON), now, now); err != nil {
		return nil, err
	}
	lines := []string{"connection " + s.ID + ": created from host settings"}
	for _, name := range s.Capabilities {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO connection_capabilities (connection_id, name, enabled, config_json) VALUES (?, ?, 1, '{}')", s.ID, name); err != nil {
			return nil, err
		}
	}
	for _, c := range s.Credentials {
		if c.Value == "" {
			continue
		}
		if !encrypted {
			return nil, fmt.Errorf("db import-v2: the source %s credential %s cannot be stored in a plaintext destination; encrypt the destination first", s.ID, c.Slot)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO connection_credentials (connection_id, slot, source, value, set_at, set_by) VALUES (?, ?, 'database', ?, ?, ?)",
			s.ID, c.Slot, c.Value, c.SetAt, c.SetBy); err != nil {
			return nil, err
		}
		lines = append(lines, "connection "+s.ID+": credential "+c.Slot+" carried")
	}
	if s.Health != nil {
		var lastOK, reauth any
		if s.Health.State == "ok" {
			lastOK = s.Health.CheckedAt
		}
		if s.Health.State == "needs_reauth" {
			reauth = s.Health.CheckedAt
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE connections SET state = ?, reason = ?, checked_at = ?, last_ok_at = ?, expires_at = ?, reauth_at = ? WHERE id = ?",
			s.Health.State, s.Health.Reason, s.Health.CheckedAt, lastOK, s.Health.ExpiresAt, reauth, s.ID); err != nil {
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("connection %s: legacy health carried as %s", s.ID, s.Health.State))
	}
	return lines, nil
}

func compact(values map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range values {
		if value != "" {
			out[key] = value
		}
	}
	return out
}
