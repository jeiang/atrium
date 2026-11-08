package db

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"sync"
	"time"
)

//go:embed schema
var schemaFS embed.FS

func initSchema(ctx context.Context, q Querier) error {
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
	Skills []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Body        string `json:"body"`
	} `json:"skills"`
	Subagents []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
		Effort      string `json:"effort"`
		ToolsJSON   string `json:"tools_json"`
	} `json:"subagents"`
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
	for _, sk := range s.Skills {
		if _, err := q.ExecContext(ctx, "INSERT OR IGNORE INTO skills (name, description, body, updated_at) VALUES (?, ?, ?, ?)",
			sk.Name, sk.Description, sk.Body, ts); err != nil {
			return err
		}
	}
	for _, a := range s.Subagents {
		if _, err := q.ExecContext(ctx,
			"INSERT OR IGNORE INTO subagents (name, description, prompt, effort, tools_json, enabled, builtin, updated_at) VALUES (?, ?, ?, ?, ?, 1, 1, ?)",
			a.Name, a.Description, a.Prompt, a.Effort, a.ToolsJSON, ts); err != nil {
			return err
		}
	}
	return nil
}

var builtinSkills = sync.OnceValues(func() (map[string]bool, error) {
	raw, err := schemaFS.ReadFile("schema/seeds.json")
	if err != nil {
		return nil, err
	}
	var s seeds
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, sk := range s.Skills {
		names[sk.Name] = true
	}
	return names, nil
})

func BuiltinSkills() (map[string]bool, error) { return builtinSkills() }
