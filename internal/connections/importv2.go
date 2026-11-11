package connections

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

func ImportV2(ctx context.Context, tx *sql.Tx, q ImportQbit, encrypted bool, now float64) ([]string, error) {
	if q.URL == "" {
		return []string{"connection " + qbittorrentID + ": no host URL in the source; not materialized"}, nil
	}
	config, err := json.Marshal(map[string]string{"url": q.URL})
	if err != nil {
		return nil, err
	}
	transport := map[string]string{}
	if q.CAFile != "" {
		transport["ca_file"] = q.CAFile
	}
	if q.Proxy != "" {
		transport["proxy"] = q.Proxy
	}
	transportJSON, err := json.Marshal(transport)
	if err != nil {
		return nil, err
	}
	var lines []string
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO connections (id, kind, label, config_source, config_json, transport_json, revision, enabled, state, created_at, updated_at) "+
			"VALUES (?, ?, 'qBittorrent', 'host', ?, ?, 1, 1, 'untested', ?, ?)",
		qbittorrentID, qbittorrentID, string(config), string(transportJSON), now, now); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO connection_capabilities (connection_id, name, enabled, config_json) VALUES (?, 'api', 1, '{}')", qbittorrentID); err != nil {
		return nil, err
	}
	lines = append(lines, "connection "+qbittorrentID+": created from host settings")
	if q.Key != "" {
		if !encrypted {
			return nil, errors.New("db import-v2: the source qBittorrent API key cannot be stored in a plaintext destination; encrypt the destination first")
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO connection_credentials (connection_id, slot, source, value, set_at, set_by) VALUES (?, 'api_key', 'database', ?, ?, ?)",
			qbittorrentID, q.Key, q.SetAt, q.SetBy); err != nil {
			return nil, err
		}
		lines = append(lines, "connection "+qbittorrentID+": credential api_key carried")
	}
	if q.Health != nil {
		var lastOK, reauth any
		if q.Health.State == "ok" {
			lastOK = q.Health.CheckedAt
		}
		if q.Health.State == "needs_reauth" {
			reauth = q.Health.CheckedAt
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE connections SET state = ?, reason = ?, checked_at = ?, last_ok_at = ?, expires_at = ?, reauth_at = ? WHERE id = ?",
			q.Health.State, q.Health.Reason, q.Health.CheckedAt, lastOK, q.Health.ExpiresAt, reauth, qbittorrentID); err != nil {
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("connection %s: legacy health carried as %s", qbittorrentID, q.Health.State))
	}
	return lines, nil
}
