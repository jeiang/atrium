package nightly

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/tool"
	profiletools "github.com/jeiang/atrium/internal/assistant/tools/profile"
	"github.com/jeiang/atrium/internal/proposals"
)

const captureKind = "notion_capture"

const observeSchema = `{"type":"object","properties":{"subject":{"type":"string"},"value":{"type":"string"},"profile_key":{"type":"string"}},"required":["subject","value"],"additionalProperties":false}`

func observeTool() tool.Tool {
	return tool.Tool{
		Name:        "nightly_observe",
		Description: "Record one durable observation from the day (subject, value). The first sighting proposes a 'sometimes uses' wiki note; a repeat across two weeks proposes a replacement; a contradiction with the profile proposes a profile update. Never write the wiki or profile directly.",
		InputSchema: json.RawMessage(observeSchema),
		Handler:     observe,
		Effect:      tool.Write,
		Annotated:   true,
		Contexts:    tool.Kinds{tool.Nightly},
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
		id, err := propose(ctx, tc, profiletools.KindProfileUpdate, "Profile update: "+profileKey, map[string]any{"key": profileKey, "value": value, "previous": profile})
		if err != nil {
			return nil, err
		}
		return map[string]any{"proposal_id": id, "kind": profiletools.KindProfileUpdate}, nil
	}
	var old string
	var first float64
	oldErr := tc.DB.Read.QueryRowContext(ctx,
		"SELECT value, first_seen FROM nightly_observations WHERE subject = ? AND value <> ? ORDER BY first_seen LIMIT 1", subject, value).Scan(&old, &first)
	switch {
	case oldErr == nil && first <= now-14*86400:
		note := fmt.Sprintf("now uses %s (was %s)", value, old)
		id, err := propose(ctx, tc, captureKind, "Wiki note: "+note, map[string]any{"note": note, "subject": subject, "sources": []string{}})
		if err != nil {
			return nil, err
		}
		return map[string]any{"proposal_id": id, "kind": captureKind, "note": note}, nil
	case count == 1:
		note := "sometimes uses " + value
		id, err := propose(ctx, tc, captureKind, "Wiki note: "+note, map[string]any{"note": note, "subject": subject, "sources": []string{}})
		if err != nil {
			return nil, err
		}
		return map[string]any{"proposal_id": id, "kind": captureKind, "note": note}, nil
	}
	return map[string]any{"recorded": true, "count": count}, nil
}

func propose(ctx context.Context, tc *tool.Context, kind, title string, payload map[string]any) (int64, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	sum := sha256.Sum256(raw)
	id, _, err := proposals.Create(ctx, tc.DB, kind, nil, title, payload, payload, hex.EncodeToString(sum[:]), tc.Now())
	return id, err
}
