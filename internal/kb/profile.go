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

type profileRow struct {
	rec     Record
	private bool
}

func (r profileRow) visible(projectID int64) bool {
	if !r.rec.Private && !r.private {
		return true
	}
	return r.rec.ProjectID != nil && *r.rec.ProjectID == projectID
}

func profileRows(ctx context.Context, q db.Querier, projectID int64, all bool) ([]profileRow, error) {
	query := "SELECT " + columns + ", COALESCE(p.private, 0)" + fromClause() + " WHERE r.pinned = 1 AND r.retracted_at IS NULL"
	args := []any{}
	if !all {
		clause, pargs := privateClause("r", projectID)
		query += " AND " + clause
		args = append(args, pargs...)
	}
	query += " ORDER BY r.title, r.id"
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (profileRow, error) {
		var row profileRow
		var private int64
		rec, err := scanExtra(r, &private)
		row.rec, row.private = rec, private != 0
		return row, err
	})
}

func profileEntries(found []profileRow, budget, projectID int64, review bool) []ProfileRecord {
	total := tokens.Estimate(ProfileHeader)
	out := make([]ProfileRecord, 0, len(found))
	for _, row := range found {
		if !review && !row.visible(projectID) {
			continue
		}
		entry := ProfileRecord{Record: row.rec}
		switch {
		case !row.rec.Confirmed():
			entry.Reason = "unconfirmed"
		case row.rec.Marks&(tool.Untrusted|tool.PII|tool.Messages) != 0:
			entry.Reason = "marked"
		case review && !row.visible(projectID):
			entry.Reason = "private"
		default:
			entry.Tokens = tokens.Estimate("- " + row.rec.Title + ": " + row.rec.Body)
			if total+entry.Tokens > budget {
				entry.Reason = "over budget"
			} else {
				entry.InPrompt = true
				total += entry.Tokens
			}
		}
		out = append(out, entry)
	}
	return out
}

func ProfileEntries(ctx context.Context, q db.Querier, budget, projectID int64) ([]ProfileRecord, error) {
	found, err := profileRows(ctx, q, projectID, false)
	if err != nil {
		return nil, err
	}
	return profileEntries(found, budget, projectID, false), nil
}

func ProfileReview(ctx context.Context, q db.Querier, budget int64) ([]ProfileRecord, error) {
	found, err := profileRows(ctx, q, 0, true)
	if err != nil {
		return nil, err
	}
	return profileEntries(found, budget, 0, true), nil
}

func ProfileBlock(ctx context.Context, q db.Querier, projectID int64) (string, error) {
	budget := int64(assistant.TuningAgentKbProfileTokens)
	entries, err := ProfileEntries(ctx, q, budget, projectID)
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
