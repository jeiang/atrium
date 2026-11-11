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
	Source       string
	Enabled      *bool
	Config       map[string]string
	Transport    map[string]string
	Capabilities []string
	Credentials  []ImportCredential
	Results      []Result
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
	source := s.Source
	if source == "" {
		source = SourceOperator
	}
	enabled, state := 1, "untested"
	if s.Enabled != nil && !*s.Enabled {
		enabled, state = 0, "disabled"
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO connections (id, kind, label, config_source, config_json, transport_json, revision, enabled, state, created_at, updated_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?) "+
			"ON CONFLICT(id) DO UPDATE SET label = excluded.label, config_json = excluded.config_json, transport_json = excluded.transport_json, "+
			"revision = connections.revision + 1, enabled = excluded.enabled, state = excluded.state, reason = '', checked_at = NULL, last_ok_at = NULL, "+
			"expires_at = NULL, reauth_at = NULL, results_json = '{}', updated_at = excluded.updated_at "+
			"WHERE connections.config_source = 'host' OR ?",
		s.ID, kind, s.Label, source, string(configJSON), string(transportJSON), enabled, state, now, now, boolInt(builtinIDs[s.ID])); err != nil {
		return nil, err
	}
	lines := []string{"connection " + s.ID + ": created from " + source + " settings"}
	if _, err := tx.ExecContext(ctx, "DELETE FROM connection_capabilities WHERE connection_id = ?", s.ID); err != nil {
		return nil, err
	}
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
		res, err := tx.ExecContext(ctx,
			"INSERT INTO connection_credentials (connection_id, slot, source, value, set_at, set_by) VALUES (?, ?, 'database', ?, ?, ?) "+
				"ON CONFLICT(connection_id,slot) DO NOTHING",
			s.ID, c.Slot, c.Value, c.SetAt, c.SetBy)
		if err != nil {
			return nil, err
		}
		if n, err := res.RowsAffected(); err != nil {
			return nil, err
		} else if n == 0 {
			continue
		}
		lines = append(lines, "connection "+s.ID+": credential "+c.Slot+" carried")
	}
	if len(s.Results) > 0 {
		raw, err := json.Marshal(s.Results)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE connections SET results_json = ? WHERE id = ?", string(raw), s.ID); err != nil {
			return nil, err
		}
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
