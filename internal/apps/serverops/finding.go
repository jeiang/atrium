package serverops

import (
	"context"
	"database/sql"
	"slices"

	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/tool"
)

const FindingTTL = 30 * 24 * 3600.0

func CheckFinding(fact, severity, title, body string, delta []string) error {
	if !slices.Contains(delta, fact) {
		return tool.Error("fact is not in this run's change set")
	}
	if severity != "info" && severity != "warning" && severity != "critical" {
		return tool.Error("severity must be info, warning or critical")
	}
	if title == "" {
		return tool.Error("title is required")
	}
	if body == "" {
		return tool.Error("body is required")
	}
	if len([]rune(title)) > 120 {
		return tool.Error("title is limited to 120 characters")
	}
	if len([]rune(body)) > 1000 {
		return tool.Error("body is limited to 1000 characters")
	}
	return nil
}

type Finding struct {
	Fact, Severity, Title, Body string
	Marks                       tool.Marks
	ThreadID, RunID             int64
	Tool                        string
	Now                         float64
}

func WriteFindingTx(ctx context.Context, store *ledger.Store, tx *sql.Tx, f Finding) (kb.Record, error) {
	var rec kb.Record
	_, err := store.ApplyLocalTx(ctx, tx, kb.OpsEffect(f.Tool, f.ThreadID, f.RunID), func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		next, res, err := kb.WriteOpsPageTx(ctx, tx, kb.OpsPage{
			Fact: f.Fact, Title: f.Title, Body: f.Severity + " · " + f.Fact + "\n\n" + f.Body, Marks: f.Marks,
			ThreadID: f.ThreadID, RunID: f.RunID, Expires: f.Now + FindingTTL, Now: f.Now,
		})
		rec = next
		return res, err
	})
	return rec, err
}
