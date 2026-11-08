package notiontools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/tool"
	profiletools "github.com/jeiang/atrium/internal/assistant/tools/profile"
)

const observeName = "nightly_observe"

const observeSchema = `{"type":"object","properties":{"subject":{"type":"string"},"value":{"type":"string"},"profile_key":{"type":"string"}},"required":["subject","value"],"additionalProperties":false}`

func observeTool() tool.Tool {
	return tool.Tool{
		Name:        observeName,
		Description: "Record one durable observation from the day (subject, value). The first sighting adds a 'sometimes uses' note to the Wiki Log; a repeat across two weeks adds a replacement note; a contradiction with the profile updates the profile. Each write is logged and undoable by the operator.",
		InputSchema: json.RawMessage(observeSchema),
		Handler:     observe,
		Effect:      tool.WriteUndo, Undo: "notion_page_restore, profile_restore",
		Annotated: true,
		Contexts:  tool.Kinds{tool.Nightly},
	}
}

func observe(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	subject, ok, err := tool.TextArg(args, "subject", true)
	if err != nil || !ok {
		return nil, tool.Error("subject is required")
	}
	value, ok, err := tool.TextArg(args, "value", true)
	if err != nil || !ok {
		return nil, tool.Error("value is required")
	}
	profileKey, _, err := tool.TextArg(args, "profile_key", false)
	if err != nil {
		return nil, err
	}
	if profileKey == "" {
		profileKey = subject
	}
	now := tc.Now()
	if _, err := tc.DB.Write.ExecContext(ctx,
		"INSERT INTO nightly_observations (subject, value, first_seen, last_seen, count) VALUES (?, ?, ?, ?, 1) "+
			"ON CONFLICT(subject, value) DO UPDATE SET last_seen = excluded.last_seen, count = count + 1",
		subject, value, now, now); err != nil {
		return nil, err
	}
	var count int
	if err := tc.DB.Read.QueryRowContext(ctx, "SELECT count FROM nightly_observations WHERE subject = ? AND value = ?", subject, value).Scan(&count); err != nil {
		return nil, err
	}
	var profile string
	profileErr := tc.DB.Read.QueryRowContext(ctx, "SELECT value FROM agent_profile WHERE key = ?", profileKey).Scan(&profile)
	if profileErr == nil && profile != value {
		out, err := profiletools.Write(ctx, tc, observeName, profileKey, value)
		if err != nil {
			return nil, err
		}
		out["updated"] = "profile"
		return out, nil
	}
	var old string
	var first float64
	oldErr := tc.DB.Read.QueryRowContext(ctx,
		"SELECT value, first_seen FROM nightly_observations WHERE subject = ? AND value <> ? ORDER BY first_seen LIMIT 1", subject, value).Scan(&old, &first)
	switch {
	case oldErr == nil && first <= now-14*86400:
		return Capture(ctx, tc, observeName, fmt.Sprintf("now uses %s (was %s)", value, old))
	case count == 1:
		return Capture(ctx, tc, observeName, "sometimes uses "+value)
	}
	return map[string]any{"recorded": true, "count": count}, nil
}
