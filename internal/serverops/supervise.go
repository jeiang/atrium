package serverops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	DeltaMarker = "OPS_DELTA "
	WorkerRole  = "ops"
	highPct     = 90.0
)

type change struct {
	Fact   string   `json:"fact"`
	Metric string   `json:"metric"`
	From   string   `json:"from"`
	To     string   `json:"to"`
	Alerts []string `json:"alerts,omitempty"`
}

type delta struct {
	Changes []change `json:"changes"`
}

type sample struct {
	host, metric string
	value        *float64
	state        *string
	detail       *string
}

func WorkerThreadID(ctx context.Context, q db.Querier) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx,
		"SELECT id FROM threads WHERE kind = ? AND role = ? AND deleted_at IS NULL ORDER BY id DESC LIMIT 1",
		threads.Worker, WorkerRole).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func workerThread(ctx context.Context, d *db.DB, now float64) (int64, error) {
	id, err := WorkerThreadID(ctx, d.Read)
	if err != nil || id != 0 {
		return id, err
	}
	title, role := "Ops", WorkerRole
	return threads.Create(ctx, d.Write, now, threads.Spec{Kind: threads.Worker, Title: &title, Role: &role})
}

func Supervise(ctx context.Context, e worker.Env) error {
	d := e.Service.DB
	now := e.Now()
	changes, err := detect(ctx, d.Read)
	if err != nil {
		return err
	}
	if err := resolveFindings(ctx, d, now); err != nil {
		return err
	}
	if err := pruneFindings(ctx, d, now); err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	threadID, err := workerThread(ctx, d, now)
	if err != nil {
		return err
	}
	fingerprint := fingerprint(changes)
	last, err := lastFingerprint(ctx, d.Read, threadID)
	if err != nil {
		return err
	}
	if last == fingerprint {
		return nil
	}
	set := assistant.Read(ctx, d.Read)
	if err := set.Err(); err != nil {
		return err
	}
	budget := set.Float(KeyWorkerMonthlyUSD)
	loc, err := config.From(ctx).Location(e.Service.Env)
	if err != nil {
		return err
	}
	spend, err := usage.ThreadMonthToDate(ctx, d.Read, threadID, now, loc)
	if err != nil {
		return err
	}
	if spend >= budget {
		return nil
	}
	text, marks := deltaText(changes)
	if marks != 0 {
		if err := threads.AddMarks(ctx, d.Write, threadID, now, marks); err != nil {
			return err
		}
	}
	maxCost := set.Float(KeyWorkerMaxRunUSD)
	steps := int64(set.Int("max_steps_worker"))
	extra := prompts.Worker
	if _, err := e.Service.StartRun(ctx, threadID, text, service.Start{Kind: threads.Worker, Limits: journal.Limits{
		MaxSteps: &steps, MaxCostUSD: &maxCost, SystemExtra: &extra,
	}}); err != nil {
		if errors.As(err, new(service.RunActive)) {
			return nil
		}
		return err
	}
	return nil
}

func detect(ctx context.Context, q db.Querier) ([]change, error) {
	rows, err := q.QueryContext(ctx, "SELECT host, metric, value, state, detail FROM ops_samples ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	latest := map[string]sample{}
	previous := map[string]sample{}
	for rows.Next() {
		var s sample
		if err := rows.Scan(&s.host, &s.metric, &s.value, &s.state, &s.detail); err != nil {
			return nil, err
		}
		key := s.host + ":" + s.metric
		if _, ok := latest[key]; !ok {
			latest[key] = s
		} else if _, ok := previous[key]; !ok {
			previous[key] = s
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(latest))
	for key := range latest {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out []change
	for _, key := range keys {
		now, ok := latest[key]
		if !ok {
			continue
		}
		before, ok := previous[key]
		if !ok {
			continue
		}
		c, changed := compare(before, now)
		if changed {
			out = append(out, c)
		}
	}
	return out, nil
}

func compare(before, now sample) (change, bool) {
	c := change{Fact: now.host + ":" + now.metric, Metric: now.metric}
	switch now.metric {
	case MetricUp:
		from, to := stateOf(before), stateOf(now)
		if from == to {
			return c, false
		}
		c.From, c.To = from, to
		return c, true
	case MetricMemory, MetricDisk:
		from, to := high(before.value), high(now.value)
		if from == to {
			return c, false
		}
		c.From, c.To = threshold(from), threshold(to)
		return c, true
	case MetricFailedUnits:
		from, to := failed(before.value), failed(now.value)
		if from == to {
			return c, false
		}
		c.From, c.To = failedLabel(from), failedLabel(to)
		return c, true
	case MetricAlerts:
		from, to := alertNames(before.detail), alertNames(now.detail)
		if slices.Equal(from, to) {
			return c, false
		}
		c.From, c.To = alertsLabel(from), alertsLabel(to)
		c.Alerts = to
		return c, true
	}
	return c, false
}

func stateOf(s sample) string {
	if s.state != nil {
		return *s.state
	}
	if s.value != nil && *s.value != 0 {
		return "up"
	}
	return "down"
}

func high(value *float64) bool { return value != nil && *value >= highPct }

func failed(value *float64) bool { return value != nil && *value > 0 }

func threshold(v bool) string {
	if v {
		return "high"
	}
	return "normal"
}

func failedLabel(v bool) string {
	if v {
		return "failed"
	}
	return "ok"
}

func alertsLabel(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return "firing"
}

func alertNames(detail *string) []string {
	if detail == nil {
		return nil
	}
	var parsed struct {
		Alerts []string `json:"alerts"`
	}
	if err := json.Unmarshal([]byte(*detail), &parsed); err != nil {
		return nil
	}
	sort.Strings(parsed.Alerts)
	return parsed.Alerts
}

func fingerprint(changes []change) string {
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		parts = append(parts, c.Fact+"|"+c.From+"|"+c.To+"|"+strings.Join(c.Alerts, ","))
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

func deltaText(changes []change) (string, tool.Marks) {
	payload, err := json.Marshal(delta{Changes: changes})
	if err != nil {
		payload = []byte(`{"changes":[]}`)
	}
	var lines []string
	var marks tool.Marks
	for _, c := range changes {
		switch c.Metric {
		case MetricUp:
			lines = append(lines, fmt.Sprintf("%s is %s (was %s).", c.Fact, c.To, c.From))
		case MetricMemory, MetricDisk:
			lines = append(lines, fmt.Sprintf("%s crossed %s (now %s).", c.Fact, thresholdLabel(c.Metric), c.To))
		case MetricFailedUnits:
			lines = append(lines, fmt.Sprintf("%s is %s.", c.Fact, c.To))
		case MetricAlerts:
			quoted := make([]string, 0, len(c.Alerts))
			for _, name := range c.Alerts {
				quoted = append(quoted, fmt.Sprintf("%q", name))
			}
			if len(quoted) > 0 {
				lines = append(lines, fmt.Sprintf("%s: alerts firing: %s.", c.Fact, strings.Join(quoted, ", ")))
				marks |= tool.Untrusted
			} else {
				lines = append(lines, fmt.Sprintf("%s: no alerts firing.", c.Fact))
			}
		}
	}
	sort.Strings(lines)
	return DeltaMarker + string(payload) + "\n" + strings.Join(lines, "\n"), marks
}

func thresholdLabel(metric string) string {
	if metric == MetricDisk {
		return "the disk threshold"
	}
	return "the memory threshold"
}

func lastFingerprint(ctx context.Context, q db.Querier, threadID int64) (string, error) {
	var raw string
	err := q.QueryRowContext(ctx,
		"SELECT content_json FROM thread_messages WHERE thread_id = ? AND role = 'user' AND superseded = 0 "+
			"AND content_json LIKE '%OPS_DELTA %' ORDER BY seq DESC LIMIT 1",
		threadID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
		return "", err
	}
	for _, block := range blocks {
		if block.Type != "text" || !strings.HasPrefix(block.Text, DeltaMarker) {
			continue
		}
		payload := strings.TrimPrefix(block.Text, DeltaMarker)
		if i := strings.IndexByte(payload, '\n'); i >= 0 {
			payload = payload[:i]
		}
		var d delta
		if err := json.Unmarshal([]byte(payload), &d); err != nil {
			return "", err
		}
		return fingerprint(d.Changes), nil
	}
	return "", nil
}

func resolveFindings(ctx context.Context, d *db.DB, now float64) error {
	rows, err := d.Read.QueryContext(ctx, "SELECT id, fact FROM ops_findings WHERE resolved_at IS NULL")
	if err != nil {
		return err
	}
	type finding struct {
		id   int64
		fact string
	}
	var findings []finding
	for rows.Next() {
		var f finding
		if err := rows.Scan(&f.id, &f.fact); err != nil {
			_ = rows.Close()
			return err
		}
		findings = append(findings, f)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, f := range findings {
		normal, err := normalFact(ctx, d.Read, f.fact)
		if err != nil {
			return err
		}
		if !normal {
			continue
		}
		if _, err := d.Write.ExecContext(ctx,
			"UPDATE ops_findings SET resolved_at = ?, resolved_reason = 'normal' WHERE id = ? AND resolved_at IS NULL", now, f.id); err != nil {
			return err
		}
	}
	return nil
}

func pruneFindings(ctx context.Context, d *db.DB, now float64) error {
	_, err := d.Write.ExecContext(ctx, "DELETE FROM ops_findings WHERE resolved_at IS NOT NULL AND resolved_at < ?", now-30*24*3600)
	return err
}

func normalFact(ctx context.Context, q db.Querier, fact string) (bool, error) {
	host, metric, ok := splitFact(fact)
	if !ok {
		return false, nil
	}
	var value *float64
	var state *string
	err := q.QueryRowContext(ctx,
		"SELECT value, state FROM ops_samples WHERE host = ? AND metric = ? ORDER BY id DESC LIMIT 1", host, metric).Scan(&value, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch metric {
	case MetricUp:
		return state != nil && *state == "up", nil
	case MetricMemory, MetricDisk:
		return value != nil && *value < highPct, nil
	case MetricFailedUnits, MetricAlerts:
		return value != nil && *value == 0, nil
	}
	return false, nil
}

func splitFact(fact string) (string, string, bool) {
	for _, metric := range Metrics {
		suffix := ":" + metric
		if strings.HasSuffix(fact, suffix) {
			return strings.TrimSuffix(fact, suffix), metric, true
		}
	}
	return "", "", false
}
