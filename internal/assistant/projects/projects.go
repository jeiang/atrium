// Package projects is agent/projects.py: the operator's projects, which group chat threads, give them default
// options and a standing brief, and name the wiki hub page their knowledge goes to.
package projects

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/assistant/store"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/notionid"
	"github.com/jeiang/atrium/internal/py"
)

const (
	NameChars        = 80
	InstructionChars = 20_000
)

// ProjectError is a refused project field.
type ProjectError string

func (e ProjectError) Error() string { return string(e) }

// Project is a projects row with its thread count and last activity.
type Project struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Instructions string     `json:"instructions"`
	Effort       string     `json:"effort"`
	ContextLimit int64      `json:"context_limit"`
	WikiHubID    *string    `json:"wiki_hub_id"`
	Archived     store.Flag `json:"archived"`
	CreatedAt    float64    `json:"created_at"`
	UpdatedAt    float64    `json:"updated_at"`
	ThreadCount  int64      `json:"thread_count"`
	LastActivity *float64   `json:"last_activity"`
}

const listSQL = "SELECT p.id, p.name, p.instructions, p.effort, p.context_limit, p.wiki_hub_id, p.archived, p.created_at, p.updated_at, " +
	"(SELECT COUNT(*) FROM threads t WHERE t.project_id = p.id AND t.kind = 'chat' AND t.archived = 0) AS thread_count, " +
	"(SELECT MAX(t.updated_at) FROM threads t WHERE t.project_id = p.id AND t.kind = 'chat') AS last_activity " +
	"FROM projects p"

func scan(r interface{ Scan(...any) error }) (Project, error) {
	var p Project
	err := r.Scan(&p.ID, &p.Name, &p.Instructions, &p.Effort, &p.ContextLimit, &p.WikiHubID, &p.Archived, &p.CreatedAt,
		&p.UpdatedAt, &p.ThreadCount, &p.LastActivity)
	return p, err
}

// Fields are the editable fields of a project as the operator submitted them.
type Fields struct {
	Name         string
	Instructions string
	Effort       string
	ContextLimit any
	WikiHubID    string
}

type cleaned struct {
	Name         string
	Instructions string
	Effort       string
	ContextLimit int64
	WikiHubID    *string
}

func clean(f Fields) (cleaned, error) {
	name := py.Strip(f.Name)
	if name == "" {
		return cleaned{}, ProjectError("Give the project a name.")
	}
	if utf8.RuneCountInString(name) > NameChars {
		return cleaned{}, ProjectError(fmt.Sprintf("The name is limited to %d characters.", NameChars))
	}
	instructions := py.Strip(f.Instructions)
	if utf8.RuneCountInString(instructions) > InstructionChars {
		return cleaned{}, ProjectError(fmt.Sprintf("Instructions are limited to %d characters.", InstructionChars))
	}
	effort, err := threads.CleanEffort(f.Effort)
	if err != nil {
		return cleaned{}, ProjectError(err.Error())
	}
	limit, err := threads.CleanContextLimit(f.ContextLimit)
	if err != nil {
		return cleaned{}, ProjectError(err.Error())
	}
	out := cleaned{Name: name, Instructions: instructions, Effort: effort, ContextLimit: limit}
	if hub := py.Strip(f.WikiHubID); hub != "" {
		found := notionid.IDsInText(hub)
		if len(found) == 0 {
			return cleaned{}, ProjectError("The wiki hub must be a Notion page id or URL.")
		}
		out.WikiHubID = &found[0]
	}
	return out, nil
}

// Create is create: it validates the fields and adds the project.
func Create(ctx context.Context, q store.Querier, now float64, f Fields) (int64, error) {
	v, err := clean(f)
	if err != nil {
		return 0, err
	}
	res, err := q.ExecContext(ctx,
		"INSERT INTO projects (name, instructions, effort, context_limit, wiki_hub_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		v.Name, v.Instructions, v.Effort, v.ContextLimit, v.WikiHubID, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Update is update: false when there is no such project.
func Update(ctx context.Context, q store.Querier, projectID int64, now float64, f Fields) (bool, error) {
	v, err := clean(f)
	if err != nil {
		return false, err
	}
	return changed(q.ExecContext(ctx,
		"UPDATE projects SET name = ?, instructions = ?, effort = ?, context_limit = ?, wiki_hub_id = ?, updated_at = ? WHERE id = ?",
		v.Name, v.Instructions, v.Effort, v.ContextLimit, v.WikiHubID, now, projectID))
}

func changed(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Get is get: nil when there is no such project.
func Get(ctx context.Context, q store.Querier, projectID int64) (*Project, error) {
	p, err := scan(q.QueryRowContext(ctx, listSQL+" WHERE p.id = ?", projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// List is list_projects: the active (or archived) projects, most recently used first.
func List(ctx context.Context, q store.Querier, archived bool) ([]Project, error) {
	rows, err := q.QueryContext(ctx,
		listSQL+" WHERE p.archived = ? ORDER BY COALESCE(last_activity, p.updated_at) DESC, p.id DESC", store.Bit(archived))
	if err != nil {
		return nil, err
	}
	return store.Collect(rows, func(r *sql.Rows) (Project, error) { return scan(r) })
}

// SetArchived is set_archived.
func SetArchived(ctx context.Context, q store.Querier, projectID int64, value bool) (bool, error) {
	return changed(q.ExecContext(ctx, "UPDATE projects SET archived = ? WHERE id = ?", store.Bit(value), projectID))
}

// SetHub is set_hub.
func SetHub(ctx context.Context, q store.Querier, projectID int64, pageID string, now float64) (bool, error) {
	return changed(q.ExecContext(ctx, "UPDATE projects SET wiki_hub_id = ?, updated_at = ? WHERE id = ?", pageID, now, projectID))
}

// MoveThread is move_thread: a nil projectID takes the thread out of its project; an archived or unknown
// project is refused. False when the thread is not a chat.
func MoveThread(ctx context.Context, q store.Querier, threadID int64, projectID *int64) (bool, error) {
	if projectID != nil {
		var one int
		err := q.QueryRowContext(ctx, "SELECT 1 FROM projects WHERE id = ? AND archived = 0", *projectID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return false, ProjectError("unknown project")
		}
		if err != nil {
			return false, err
		}
	}
	return changed(q.ExecContext(ctx, "UPDATE threads SET project_id = ? WHERE id = ? AND kind = 'chat'", projectID, threadID))
}

// HubMarkdown is hub_markdown: the first text of a new wiki hub page.
func HubMarkdown(p Project) string {
	lines := []string{fmt.Sprintf("Durable knowledge for the project “%s”: decisions, status, open questions and links.", p.Name)}
	if p.Instructions != "" {
		lines = append(lines, "", "## Project brief", "", p.Instructions)
	}
	return strings.Join(lines, "\n")
}

// SystemBlock is system_block: the per-project prompt text, appended after the cached prefix. Nil when the
// thread has no project or the project has nothing to say.
func SystemBlock(ctx context.Context, q store.Querier, t threads.Thread) (*string, error) {
	if t.ProjectID == nil || *t.ProjectID == 0 {
		return nil, nil
	}
	p, err := Get(ctx, q, *t.ProjectID)
	if err != nil || p == nil {
		return nil, err
	}
	var parts []string
	if p.Instructions != "" {
		parts = append(parts, fmt.Sprintf("This thread belongs to the operator's project “%s”. Follow the project's instructions:\n<project_instructions>\n%s\n</project_instructions>", p.Name, p.Instructions))
	}
	if p.WikiHubID != nil && *p.WikiHubID != "" {
		parts = append(parts, fmt.Sprintf("The project's wiki hub page has id %s. Keep it updated with durable project knowledge "+
			"(decisions, status, open questions, sources) as you learn it: load the wiki-capture skill (`/skill:wiki-capture`) and follow it, "+
			"updating that hub page or pages under it rather than creating duplicates.", *p.WikiHubID))
	}
	if len(parts) == 0 {
		return nil, nil
	}
	text := strings.Join(parts, "\n\n")
	return &text, nil
}
