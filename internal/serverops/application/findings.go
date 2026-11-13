package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/serverops"
	"github.com/jeiang/atrium/internal/tool"
)

const sampleRowCap = 2000

type SampleFilter struct {
	Host   string
	Metric string
	Hours  int64
}

type ObservationsInput struct {
	Samples   *SampleFilter
	Findings  int64
	FindingID int64
}

type Observations struct {
	Samples  []serverops.SampleRow  `json:"samples,omitempty"`
	Findings []serverops.FindingRow `json:"findings,omitempty"`
	Finding  *serverops.FindingRow  `json:"finding,omitempty"`
}

type FindingInput struct {
	Fact     string
	Severity string
	Title    string
	Body     string
}

type FindingResult struct {
	ID       int64   `json:"id"`
	Fact     string  `json:"fact"`
	Severity string  `json:"severity"`
	Title    string  `json:"title"`
	Written  float64 `json:"written"`
}

func (s *Service) Observations(ctx context.Context, in ObservationsInput) (application.Read[Observations], error) {
	var out Observations
	if in.Samples != nil && in.Samples.Metric != "" && !slices.Contains(serverops.Metrics, in.Samples.Metric) {
		return application.Read[Observations]{}, tool.Error("metric is unknown")
	}
	now := s.clock()
	inv, _ := application.InvocationOf(ctx)
	threadID := inv.ThreadID
	rev, err := s.snapshot(ctx, ObservationsName, struct {
		Input  ObservationsInput `json:"input"`
		Thread int64             `json:"thread"`
		Minute int64             `json:"minute"`
	}{in, threadID, int64(now) / 60}, func(q db.Querier) error {
		var err error
		switch {
		case in.Samples != nil:
			out.Samples, err = serverops.Samples(ctx, q, now-float64(in.Samples.Hours)*3600, in.Samples.Host, in.Samples.Metric, sampleRowCap)
		case in.FindingID != 0:
			out.Finding, err = serverops.FindingByID(ctx, q, in.FindingID)
			if err == nil && out.Finding == nil {
				err = tool.Error(fmt.Sprintf("finding %d is unknown", in.FindingID))
			}
		default:
			out.Findings, err = serverops.FindingsByThread(ctx, q, threadID, in.Findings)
		}
		return err
	})
	if err != nil {
		return application.Read[Observations]{}, err
	}
	return application.Read[Observations]{Revision: rev, Data: out}, nil
}

func (s *Service) Finding(ctx context.Context, in FindingInput) (application.Change[FindingResult], error) {
	inv, _ := application.InvocationOf(ctx)
	if err := s.checkFinding(ctx, inv.ThreadID, in); err != nil {
		if auditErr := s.auditAlone(ctx, FindingName, "thread:"+strconv.FormatInt(inv.ThreadID, 10), state.Invalid); auditErr != nil {
			return application.Change[FindingResult]{}, errors.Join(err, auditErr)
		}
		return application.Change[FindingResult]{}, err
	}
	marks, err := threads.MarksOfID(ctx, s.db.Read, inv.ThreadID)
	if err != nil {
		return application.Change[FindingResult]{}, err
	}
	now := s.clock()
	var id, auditID int64
	err = s.db.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = serverops.WriteFindingTx(ctx, s.ledger, tx, serverops.Finding{
			Fact: in.Fact, Severity: in.Severity, Title: in.Title, Body: in.Body, Marks: marks,
			ThreadID: inv.ThreadID, RunID: inv.RunID, Tool: "ops_finding", Now: now,
		})
		if err != nil {
			return err
		}
		auditID, err = s.state.Audit(ctx, tx, state.Entry{Operation: string(FindingName), Outcome: state.OK, Target: "ops_finding:" + strconv.FormatInt(id, 10)})
		return err
	})
	if err != nil {
		return application.Change[FindingResult]{}, err
	}
	return application.Change[FindingResult]{
		Data:    FindingResult{ID: id, Fact: in.Fact, Severity: in.Severity, Title: in.Title, Written: now},
		AuditID: auditID,
	}, nil
}

func (s *Service) checkFinding(ctx context.Context, threadID int64, in FindingInput) error {
	if len([]rune(in.Title)) > 120 {
		return tool.Error("title is limited to 120 characters")
	}
	if len([]rune(in.Body)) > 1000 {
		return tool.Error("body is limited to 1000 characters")
	}
	changed, err := deltaFacts(ctx, s.db.Read, threadID)
	if err != nil {
		return err
	}
	return serverops.CheckFinding(in.Fact, in.Severity, in.Title, in.Body, changed)
}

type delta struct {
	Changes []struct {
		Fact string `json:"fact"`
	} `json:"changes"`
}

func deltaFacts(ctx context.Context, q db.Querier, threadID int64) ([]string, error) {
	raw, err := threads.LatestUserContent(ctx, q, threadID, serverops.DeltaMarker)
	if err != nil || raw == "" {
		return nil, err
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
		return nil, err
	}
	for _, block := range blocks {
		if block.Type != "text" || !strings.HasPrefix(block.Text, serverops.DeltaMarker) {
			continue
		}
		payload := strings.TrimPrefix(block.Text, serverops.DeltaMarker)
		if i := strings.IndexByte(payload, '\n'); i >= 0 {
			payload = payload[:i]
		}
		var d delta
		if err := json.Unmarshal([]byte(payload), &d); err != nil {
			return nil, err
		}
		out := make([]string, 0, len(d.Changes))
		for _, change := range d.Changes {
			out = append(out, change.Fact)
		}
		return out, nil
	}
	return nil, nil
}
