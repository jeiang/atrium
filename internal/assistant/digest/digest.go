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
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/serverops"
	"github.com/jeiang/atrium/internal/settings"
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

func Run(ctx context.Context, svc *service.Service, nowFn func() float64, jobRunID int64, sources []attention.Source) (bool, error) {
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
		occurrence := "digest:" + day.Format("2006-01-02")
		if jobRunID != 0 {
			occurrence = fmt.Sprintf("digest:%d", jobRunID)
		}
		if _, err := notify.Send(ctx, svc.DB, notify.Message{
			Channel: notify.Assistant, Title: "Morning digest", Body: strings.Join(body, "\n"),
			Now: func() float64 { return now }, Occurrence: occurrence,
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
	var jobRun, jobID, threadID int64
	var status string
	var jobStarted float64
	err := q.QueryRowContext(ctx,
		"SELECT jr.id, jr.job_id, jr.status, COALESCE(jr.thread_id, 0), COALESCE(jr.started_at, jr.due_at) FROM job_runs jr JOIN jobs j ON j.id = jr.job_id "+
			"WHERE j.name = 'nightly' AND jr.ended_at IS NOT NULL AND jr.ended_at >= ? ORDER BY jr.id DESC LIMIT 1", now-86400).Scan(&jobRun, &jobID, &status, &threadID, &jobStarted)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.skipped = status == "skipped_budget"
	out.failed = status == "error"
	rows, err := q.QueryContext(ctx,
		"SELECT r.id, r.started_at FROM runs r JOIN threads t ON t.id = r.thread_id WHERE t.job_id = ? AND r.started_at >= ? ORDER BY r.id", jobID, jobStarted)
	if err != nil {
		return out, err
	}
	type runStart struct {
		id      int64
		started float64
	}
	runs, err := db.Collect(rows, func(r *sql.Rows) (runStart, error) {
		var run runStart
		err := r.Scan(&run.id, &run.started)
		return run, err
	})
	if err != nil {
		return out, err
	}
	if len(runs) == 0 && threadID != 0 {
		var runID int64
		var started float64
		err = q.QueryRowContext(ctx, "SELECT id, started_at FROM runs WHERE thread_id = ? ORDER BY id DESC LIMIT 1", threadID).Scan(&runID, &started)
		if errors.Is(err, sql.ErrNoRows) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		runs = []runStart{{id: runID, started: started}}
	}
	if len(runs) == 0 {
		return out, nil
	}
	out.run = runs[0].id
	runIDs := make([]int64, 0, len(runs))
	for _, run := range runs {
		runIDs = append(runIDs, run.id)
	}
	if out.new, err = kb.CountRunRecords(ctx, q, runIDs); err != nil {
		return out, err
	}
	if out.edited, err = kb.CountRunWrites(ctx, q, runIDs, "kb_write", true); err != nil {
		return out, err
	}
	if out.retracted, err = kb.CountRunWrites(ctx, q, runIDs, "kb_retract", false); err != nil {
		return out, err
	}
	if out.tasks, err = ledger.CountForRuns(ctx, q, runIDs, "task_add"); err != nil {
		return out, err
	}
	if out.lessons, err = kb.CountRunLessons(ctx, q, runIDs); err != nil {
		return out, err
	}
	if out.marked, err = kb.CountRunMarked(ctx, q, runIDs); err != nil {
		return out, err
	}
	conflicts, err := kb.RunConflicts(ctx, q, runIDs, `%"conflict"%`)
	if err != nil {
		return out, err
	}
	for _, c := range conflicts {
		out.conflicts = append(out.conflicts, conflict{id: c.ID, title: c.Title, thread: c.Thread, marks: int64(c.Marks)})
	}
	if id, err := ledger.BlockedTarget(ctx, q, "export:notion:"); err != nil {
		return out, err
	} else if id != 0 {
		out.exportID = id
	} else if id, err := ledger.BlockedTarget(ctx, q, "export:obsidian:"); err != nil {
		return out, err
	} else {
		out.exportID = id
	}
	if out.opsFinding, err = serverops.CountUnresolved(ctx, q); err != nil {
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
