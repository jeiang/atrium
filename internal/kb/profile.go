package kb

import (
	"context"
	"database/sql"
	"strings"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/tokens"
	"github.com/jeiang/atrium/internal/tool"
)

const ProfileHeader = "Operator profile (facts confirmed by the operator):"

type ProfileRecord struct {
	Record
	InPrompt bool   `json:"in_prompt"`
	Reason   string `json:"reason,omitempty"`
	Tokens   int64  `json:"tokens"`
}

func ProfileEntries(ctx context.Context, q db.Querier, budget int64) ([]ProfileRecord, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+columns+" FROM kb_records r WHERE r.pinned = 1 AND r.retracted_at IS NULL ORDER BY r.title, r.id")
	if err != nil {
		return nil, err
	}
	found, err := db.Collect(rows, func(r *sql.Rows) (Record, error) { return scan(r) })
	if err != nil {
		return nil, err
	}
	total := tokens.Estimate(ProfileHeader)
	out := make([]ProfileRecord, 0, len(found))
	for _, rec := range found {
		entry := ProfileRecord{Record: rec}
		switch {
		case !rec.Confirmed():
			entry.Reason = "unconfirmed"
		case rec.Marks&(tool.Untrusted|tool.PII|tool.Messages) != 0:
			entry.Reason = "marked"
		default:
			entry.Tokens = tokens.Estimate("- " + rec.Title + ": " + rec.Body)
			if total+entry.Tokens > budget {
				entry.Reason = "over budget"
			} else {
				entry.InPrompt = true
				total += entry.Tokens
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

func ProfileBlock(ctx context.Context, q db.Querier) (string, error) {
	r := assistant.Read(ctx, q)
	budget := int64(r.Int(assistant.KeyKBProfileTokens))
	if err := r.Err(); err != nil {
		return "", err
	}
	entries, err := ProfileEntries(ctx, q, budget)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, entry := range entries {
		if entry.InPrompt {
			lines = append(lines, "- "+entry.Title+": "+entry.Body)
		}
	}
	if len(lines) == 0 {
		return "", nil
	}
	return ProfileHeader + "\n" + strings.Join(lines, "\n"), nil
}
