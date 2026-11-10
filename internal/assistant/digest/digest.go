package digest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/block"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/writes"
)

const (
	NotifyKey = "digest_notify"
	lineCap   = 10
)

func init() {
	settings.MustRegister(settings.Value(settings.UI, settings.Def{
		Key: NotifyKey, Module: "assistant", Type: settings.Bool, Default: "true",
	}))
}

type dreaming struct {
	run        int64
	new        int
	edited     int
	retracted  int
	tasks      int
	lessons    int
	conflicts  []conflict
	marked     int
	skipped    bool
	failed     bool
	exportID   int64
	opsFinding int
}

type conflict struct {
	id     int64
	title  string
	thread *int64
	marks  int64
}

func Run(ctx context.Context, svc *service.Service, nowFn func() float64, sources []attention.Source) (bool, error) {
	now := nowFn()
	loc, err := config.From(ctx).Location(svc.Env)
	if err != nil {
		return false, err
	}
	result, err := attention.Query(ctx, svc.DB.Read, now, sources...)
	if err != nil {
		return false, err
	}
	dream, err := dreamingOf(ctx, svc.DB.Read, now)
	if err != nil {
		return false, err
	}
	today, push := lines(result, dream)
	if len(today) == 0 {
		return false, nil
	}
	if len(today) > lineCap {
		today = today[:lineCap]
	}
	if len(push) > lineCap {
		push = push[:lineCap]
	}
	day := time.Unix(int64(now), 0).In(loc)
	threadID, err := threads.Today(ctx, svc.DB, now, day)
	if err != nil {
		return false, err
	}
	if err := store(ctx, svc.DB, threadID, day.Format("2006-01-02"), today, now); err != nil {
		return false, err
	}
	r := assistant.Read(ctx, svc.DB.Read)
	enabled := r.Text(NotifyKey) != "false"
	if err := r.Err(); err != nil {
		return false, err
	}
	if enabled {
		body := make([]string, len(push))
		for i, line := range push {
			body[i] = "- " + line.Text
		}
		if _, err := notify.Send(ctx, svc.DB.Write, notify.Message{
			Channel: notify.Assistant, Title: "Morning digest", Body: strings.Join(body, "\n"),
			Now: func() float64 { return now },
		}); err != nil {
			return false, err
		}
	}
	return true, nil
}

func store(ctx context.Context, d *db.DB, threadID int64, date string, lines []block.DigestLine, now float64) error {
	block := block.DigestBlock(date, lines)
	var existing int64
	var raw string
	err := d.Read.QueryRowContext(ctx,
		"SELECT id, content_json FROM thread_messages WHERE thread_id = ? AND content_json LIKE ? ORDER BY id DESC LIMIT 1",
		threadID, `%"type":"digest"%`).Scan(&existing, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = journal.AppendMessage(ctx, d, threadID, nil, journal.User, []json.RawMessage{block}, now, nil)
		return err
	}
	if err != nil {
		return err
	}
	var content []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		return err
	}
	found, _, ok, err := blockLines(content)
	if err != nil {
		return err
	}
	if ok && found == date {
		_, err = d.Write.ExecContext(ctx, "UPDATE thread_messages SET content_json = ?, created_at = ? WHERE id = ?",
			string(block), now, existing)
		return err
	}
	_, err = journal.AppendMessage(ctx, d, threadID, nil, journal.User, []json.RawMessage{block}, now, nil)
	return err
}

func blockLines(content []json.RawMessage) (string, []block.DigestLine, bool, error) {
	return block.DigestLines(content)
}

func dreamingOf(ctx context.Context, q db.Querier, now float64) (dreaming, error) {
	var out dreaming
	var jobRun, threadID int64
	var status string
	err := q.QueryRowContext(ctx,
		"SELECT jr.id, jr.status, COALESCE(jr.thread_id, 0) FROM job_runs jr JOIN jobs j ON j.id = jr.job_id "+
			"WHERE j.name = 'nightly' AND jr.ended_at IS NOT NULL AND jr.ended_at >= ? ORDER BY jr.id DESC LIMIT 1", now-86400).Scan(&jobRun, &status, &threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.skipped = status == "skipped_budget"
	out.failed = status == "error"
	if threadID == 0 {
		return out, nil
	}
	var runID int64
	var started float64
	err = q.QueryRowContext(ctx, "SELECT id, started_at FROM runs WHERE thread_id = ? ORDER BY id DESC LIMIT 1", threadID).Scan(&runID, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.run = runID
	visible := "r.private = 0 AND (r.project_id IS NULL OR r.project_id NOT IN (SELECT id FROM projects WHERE private = 1))"
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM kb_records r WHERE r.source_run_id = ? AND r.created_at >= ? AND "+visible, runID, started).Scan(&out.new); err != nil {
		return out, err
	}
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_writes w JOIN kb_records r ON w.target = 'kb:' || r.id WHERE w.run_id = ? AND w.tool = 'kb_write' AND r.created_at < ? AND "+visible, runID, started).Scan(&out.edited); err != nil {
		return out, err
	}
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_writes w JOIN kb_records r ON w.target = 'kb:' || r.id WHERE w.run_id = ? AND w.tool = 'kb_retract' AND "+visible, runID).Scan(&out.retracted); err != nil {
		return out, err
	}
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_writes WHERE run_id = ? AND tool = 'task_add'", runID).Scan(&out.tasks); err != nil {
		return out, err
	}
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM kb_records r WHERE r.source_run_id = ? AND r.retracted_at IS NULL AND r.title LIKE 'Lesson: %' AND "+visible, runID).Scan(&out.lessons); err != nil {
		return out, err
	}
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM kb_records r WHERE r.source_run_id = ? AND r.retracted_at IS NULL AND r.marks != 0 AND "+visible, runID).Scan(&out.marked); err != nil {
		return out, err
	}
	rows, err := q.QueryContext(ctx,
		"SELECT r.id, r.title, r.source_thread_id, r.marks FROM kb_records r WHERE r.source_run_id = ? AND r.retracted_at IS NULL AND r.aliases LIKE ? AND "+visible+" ORDER BY r.id LIMIT 2",
		runID, `%"conflict"%`)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var c conflict
		if err := rows.Scan(&c.id, &c.title, &c.thread, &c.marks); err != nil {
			return out, err
		}
		out.conflicts = append(out.conflicts, c)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if id, err := writes.UnknownTarget(ctx, q, "export:notion:"); err != nil {
		return out, err
	} else if id != 0 {
		out.exportID = id
	} else if id, err := writes.UnknownTarget(ctx, q, "export:obsidian:"); err != nil {
		return out, err
	} else {
		out.exportID = id
	}
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM kb_records WHERE scope = 'ops' AND origin LIKE 'finding:%' AND retracted_at IS NULL").Scan(&out.opsFinding); err != nil {
		return out, err
	}
	return out, nil
}

func markedKind(kind string) bool {
	switch kind {
	case "event_candidate", "commitment", "draft", "loop", "package":
		return true
	}
	return false
}

func lines(result attention.Result, dream dreaming) (today, push []block.DigestLine) {
	if len(result.Items) > 0 {
		counts := make([]string, 0, len(result.Groups))
		for _, group := range result.Groups {
			counts = append(counts, fmt.Sprintf("%d %s", group.Count, strings.ToLower(group.Label)))
		}
		text := strings.Join(counts, ", ")
		today = append(today, block.DigestLine{Text: text, URL: "/review"})
		push = append(push, block.DigestLine{Text: text, URL: "/review"})
	}
	shown := 0
	for _, group := range result.Groups {
		if shown == 4 {
			break
		}
		marked := map[string]int{}
		for _, item := range group.Items {
			if markedKind(item.Kind) {
				marked[item.Kind]++
				if len(push) < lineCap {
					push = append(push, block.DigestLine{Text: item.Title, URL: "/review"})
				}
				continue
			}
			if shown == 4 {
				continue
			}
			line := block.DigestLine{Text: item.Title, URL: "/review"}
			today = append(today, line)
			push = append(push, line)
			shown++
		}
		kinds := make([]string, 0, len(marked))
		for kind := range marked {
			kinds = append(kinds, kind)
		}
		sort.Strings(kinds)
		for _, kind := range kinds {
			if shown == 4 {
				break
			}
			line := block.DigestLine{Text: fmt.Sprintf("%d %s", marked[kind], label(kind)), URL: "/review"}
			today = append(today, line)
			shown++
		}
	}
	if n := dream.new + dream.edited + dream.retracted + dream.tasks; n > 0 {
		text := fmt.Sprintf("Dreaming: %d new, %d edited, %d retracted, %d tasks", dream.new, dream.edited, dream.retracted, dream.tasks)
		line := block.DigestLine{Text: text, URL: "/library/knowledge?state=unconfirmed"}
		today = append(today, line)
		push = append(push, line)
	}
	if dream.marked > 0 {
		line := block.DigestLine{Text: fmt.Sprintf("%d records from marked sources", dream.marked), URL: "/library/knowledge?state=unconfirmed"}
		today = append(today, line)
		push = append(push, line)
	}
	for _, c := range dream.conflicts {
		if c.marks != 0 {
			continue
		}
		text := fmt.Sprintf("K%d may be outdated", c.id)
		if c.thread != nil {
			text += fmt.Sprintf(" (thread %d)", *c.thread)
		}
		line := block.DigestLine{Text: text, URL: fmt.Sprintf("/library/knowledge/%d", c.id)}
		today = append(today, line)
		push = append(push, line)
	}
	if dream.lessons > 0 {
		line := block.DigestLine{Text: fmt.Sprintf("Lessons added: %d", dream.lessons), URL: "/library/knowledge?state=unconfirmed"}
		today = append(today, line)
		push = append(push, line)
	}
	if dream.skipped {
		line := block.DigestLine{Text: "Dreaming skipped: budget"}
		today = append(today, line)
		push = append(push, line)
	}
	if dream.failed {
		line := block.DigestLine{Text: "Dreaming failed"}
		today = append(today, line)
		push = append(push, line)
	}
	if dream.exportID != 0 {
		line := block.DigestLine{Text: fmt.Sprintf("Export blocked by write #%d", dream.exportID)}
		today = append(today, line)
		push = append(push, line)
	}
	if dream.opsFinding > 0 {
		line := block.DigestLine{Text: fmt.Sprintf("Ops findings: %d", dream.opsFinding)}
		today = append(today, line)
		push = append(push, line)
	}
	return today, push
}

func label(kind string) string {
	switch kind {
	case "event_candidate":
		return "event candidates"
	case "commitment":
		return "commitments"
	case "draft":
		return "drafts"
	case "loop":
		return "open loops"
	case "package":
		return "packages"
	}
	return kind
}
