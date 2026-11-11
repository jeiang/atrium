package db

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"
)

//go:embed schema
var schemaFS embed.FS

func initSchema(ctx context.Context, q Querier) error {
	if _, err := q.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", ApplicationID)); err != nil {
		return fmt.Errorf("db: application id: %w", err)
	}
	scripts, err := fs.Glob(schemaFS, "schema/*.sql")
	if err != nil {
		return err
	}
	slices.Sort(scripts)
	for _, name := range scripts {
		text, err := schemaFS.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, string(text)); err != nil {
			return fmt.Errorf("db: schema %s: %w", name, err)
		}
	}
	return nil
}

type seeds struct {
	Subagents []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
		Effort      string `json:"effort"`
		ToolsJSON   string `json:"tools_json"`
	} `json:"subagents"`
}

var retired = []string{"web", "notion"}

func retire(ctx context.Context, q Querier) error {
	if len(retired) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(retired)), ",")
	args := make([]any, len(retired))
	for i, name := range retired {
		args[i] = name
	}
	_, err := q.ExecContext(ctx, "DELETE FROM subagents WHERE builtin = 1 AND name IN ("+placeholders+")", args...)
	return err
}

func seed(ctx context.Context, q Querier, now time.Time) error {
	raw, err := schemaFS.ReadFile("schema/seeds.json")
	if err != nil {
		return err
	}
	var s seeds
	if err := json.Unmarshal(raw, &s); err != nil {
		return err
	}
	ts := float64(now.UnixNano()) / 1e9
	for _, a := range s.Subagents {
		if slices.Contains(retired, a.Name) {
			continue
		}
		if _, err := q.ExecContext(ctx,
			"INSERT OR IGNORE INTO subagents (name, description, prompt, effort, tools_json, enabled, builtin, updated_at) VALUES (?, ?, ?, ?, ?, 1, 1, ?)",
			a.Name, a.Description, a.Prompt, a.Effort, a.ToolsJSON, ts); err != nil {
			return err
		}
	}
	return nil
}
