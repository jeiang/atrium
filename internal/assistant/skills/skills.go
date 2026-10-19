// Package skills is agent/skills.py: the operator's playbook library. A skill is a named, described body of
// instructions; the system prompt lists their names, `/skill:name` tokens in operator text expand into their
// bodies, and skills may reference each other. The built-in wiki skills are seeded by db.Migrate (insert or
// ignore), so an edited built-in is never reset.
package skills

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

// Limits of a skill definition and of one expansion.
const (
	DescriptionChars = 160
	BodyChars        = 20000
	ExpandMaxSkills  = 5
	ExpandMaxChars   = 20000
)

var (
	name = rx.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`, 0)
	// ref matches `/skill:name` (omp style) or `[[skill:name]]`, in skill bodies, chat messages and schedule
	// prompts.
	ref = rx.MustCompile(`(?<![\w/])/skill:([a-z0-9][a-z0-9_-]{0,47})|\[\[skill:([a-z0-9][a-z0-9_-]{0,47})\]\]`, 0)
)

// Error is SkillError(ValueError): the definition cannot be saved.
type Error string

func (e Error) Error() string { return string(e) }

// PyClass is the Python class name of the error.
func (Error) PyClass() string { return "SkillError" }

// Skill is a skills row.
type Skill struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Body        string  `json:"body"`
	UpdatedAt   float64 `json:"updated_at"`
}

const columns = "name, description, body, updated_at"

func scan(row interface{ Scan(...any) error }) (Skill, error) {
	var s Skill
	err := row.Scan(&s.Name, &s.Description, &s.Body, &s.UpdatedAt)
	return s, err
}

// List is list_skills: every skill by name; a non-empty q keeps those whose name, description or body contains
// it, ignoring ASCII case.
func List(ctx context.Context, q db.Querier, query string) ([]Skill, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT "+columns+" FROM skills WHERE ? = '' OR instr(lower(name || char(10) || description || char(10) || body), lower(?)) > 0 ORDER BY name",
		query, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Skill{}
	for rows.Next() {
		s, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Get is get: the skill, or nil when there is none.
func Get(ctx context.Context, q db.Querier, skill string) (*Skill, error) {
	s, err := scan(q.QueryRowContext(ctx, "SELECT "+columns+" FROM skills WHERE name = ?", skill))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func matches(re *rx.Regexp, s string) bool {
	m, _ := re.Match(s)
	return m != nil
}

// Save is save: it creates or replaces a skill. The name, description and body are validated as typed.
func Save(ctx context.Context, q db.Querier, skill, description, body string, now float64) error {
	skill = py.Strip(skill)
	description = strings.Join(py.Split(description), " ")
	body = py.Strip(body)
	if !matches(name, skill) {
		return Error("name must be lowercase letters, digits, - or _ (max 48 characters)")
	}
	if n := utf8.RuneCountInString(description); n == 0 || n > DescriptionChars {
		return Error(fmt.Sprintf("description is required, one line, at most %d characters", DescriptionChars))
	}
	if n := utf8.RuneCountInString(body); n == 0 || n > BodyChars {
		return Error(fmt.Sprintf("body is required, at most %d characters", BodyChars))
	}
	_, err := q.ExecContext(ctx,
		"INSERT INTO skills (name, description, body, updated_at) VALUES (?, ?, ?, ?) "+
			"ON CONFLICT(name) DO UPDATE SET description = excluded.description, body = excluded.body, updated_at = excluded.updated_at",
		skill, description, body, now)
	return err
}

// Delete is delete: whether a skill was removed.
func Delete(ctx context.Context, q db.Querier, skill string) (bool, error) {
	res, err := q.ExecContext(ctx, "DELETE FROM skills WHERE name = ?", skill)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// IndexBlock is index_block: the stable system-prompt block listing skill names; empty when there are none.
func IndexBlock(ctx context.Context, q db.Querier) (string, error) {
	rows, err := q.QueryContext(ctx, "SELECT name, description FROM skills ORDER BY name")
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var lines []string
	for rows.Next() {
		var n, d string
		if err := rows.Scan(&n, &d); err != nil {
			return "", err
		}
		lines = append(lines, "- "+n+": "+d)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", nil
	}
	return "Skills (operator-written playbooks; call load_skill(name) to read one when it applies):\n" + strings.Join(lines, "\n"), nil
}

// References is references: the skill names referenced in text, unique, in order of first appearance.
func References(text string) []string {
	found, _ := ref.FindIter(text)
	out := []string{}
	seen := map[string]bool{}
	for _, m := range found {
		n := m.Group(1)
		if n == "" {
			n = m.Group(2)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// SplitKnown is split_known: names that are skills, and names that are not.
func SplitKnown(ctx context.Context, q db.Querier, names []string) (known, unknown []string, err error) {
	known, unknown = []string{}, []string{}
	for _, n := range names {
		found, err := Get(ctx, q, n)
		if err != nil {
			return nil, nil, err
		}
		if found != nil {
			known = append(known, n)
		} else {
			unknown = append(unknown, n)
		}
	}
	return known, unknown, nil
}

// Refs is references_of's result: the skills a skill references, split by whether they exist.
type Refs struct {
	Known   []string `json:"known"`
	Unknown []string `json:"unknown"`
}

// ReferencesOf is references_of: the skills `skill` references (itself excluded).
func ReferencesOf(ctx context.Context, q db.Querier, skill string) (Refs, error) {
	found, err := Get(ctx, q, skill)
	if err != nil {
		return Refs{}, err
	}
	var names []string
	if found != nil {
		for _, n := range References(found.Body) {
			if n != skill {
				names = append(names, n)
			}
		}
	}
	known, unknown, err := SplitKnown(ctx, q, names)
	return Refs{Known: known, Unknown: unknown}, err
}

// ReferencedBy is referenced_by: the skills whose body references `skill`, by name.
func ReferencedBy(ctx context.Context, q db.Querier, skill string) ([]string, error) {
	all, err := List(ctx, q, "")
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, s := range all {
		if s.Name == skill {
			continue
		}
		for _, n := range References(s.Body) {
			if n == skill {
				out = append(out, s.Name)
				break
			}
		}
	}
	return out, nil
}

// Listed is a skill with its references, as with_references decorates it.
type Listed struct {
	Skill
	References   Refs     `json:"references"`
	ReferencedBy []string `json:"referenced_by"`
}

// WithReferences is with_references.
func WithReferences(ctx context.Context, q db.Querier, skills []Skill) ([]Listed, error) {
	out := make([]Listed, 0, len(skills))
	for _, s := range skills {
		refs, err := ReferencesOf(ctx, q, s.Name)
		if err != nil {
			return nil, err
		}
		by, err := ReferencedBy(ctx, q, s.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, Listed{Skill: s, References: refs, ReferencedBy: by})
	}
	return out, nil
}

// LoadedInThread is loaded_in_thread: the skills already in the model's context for the thread, expanded into
// a message or read with load_skill.
func LoadedInThread(ctx context.Context, q db.Querier, threadID int64) (map[string]bool, error) {
	names := map[string]bool{}
	rows, err := q.QueryContext(ctx, "SELECT skills_json FROM runs WHERE thread_id = ? AND skills_json IS NOT NULL", threadID)
	if err != nil {
		return nil, err
	}
	var stored []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		stored = append(stored, raw)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, raw := range stored {
		var list []string
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			return nil, fmt.Errorf("skills: runs.skills_json %s: %w", raw, err)
		}
		for _, n := range list {
			names[n] = true
		}
	}
	rows, err = q.QueryContext(ctx,
		"SELECT c.args_json FROM tool_calls c JOIN runs r ON r.id = c.run_id WHERE r.thread_id = ? AND c.name = 'load_skill' AND c.state = 'done'",
		threadID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var args struct {
			Name any `json:"name"`
		}
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			return nil, fmt.Errorf("skills: tool_calls.args_json %s: %w", raw, err)
		}
		if n, ok := args.Name.(string); ok {
			names[n] = true
		}
	}
	return names, rows.Err()
}

// Related is one skill a loaded skill references: its name and description.
type Related struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// RelatedTo is related: the skills `skill` references that are not yet loaded, and the unknown names it
// references.
func RelatedTo(ctx context.Context, q db.Querier, skill string, loaded map[string]bool) ([]Related, []string, error) {
	refs, err := ReferencesOf(ctx, q, skill)
	if err != nil {
		return nil, nil, err
	}
	rows := []Related{}
	for _, n := range refs.Known {
		if loaded[n] {
			continue
		}
		found, err := Get(ctx, q, n)
		if err != nil {
			return nil, nil, err
		}
		if found != nil {
			rows = append(rows, Related{Name: n, Description: found.Description})
		}
	}
	return rows, refs.Unknown, nil
}

// Closure is closure: breadth-first over references from names, the skills in order, the unknown names and the
// names the caps left out. Visited names are never queued twice, so reference cycles terminate.
func Closure(ctx context.Context, q db.Querier, names []string, maxSkills, maxChars int) (chosen []Skill, unknown, skipped []string, err error) {
	var queue []string
	seen := map[string]bool{}
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			queue = append(queue, n)
		}
	}
	chosen, unknown, skipped = []Skill{}, []string{}, []string{}
	chars := 0
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		found, err := Get(ctx, q, n)
		if err != nil {
			return nil, nil, nil, err
		}
		if found == nil {
			unknown = append(unknown, n)
			continue
		}
		size := utf8.RuneCountInString(found.Body)
		if len(chosen) >= maxSkills || chars+size > maxChars {
			skipped = append(skipped, n)
			continue
		}
		chosen = append(chosen, *found)
		chars += size
		for _, r := range References(found.Body) {
			if !seen[r] {
				seen[r] = true
				queue = append(queue, r)
			}
		}
	}
	return chosen, unknown, skipped, nil
}

// Block is block: one skill as a delimited block.
func Block(s Skill) string {
	return `<skill name="` + s.Name + `">` + "\n" + s.Body + "\n</skill>"
}

// Expand is expand: the bodies of the skills referenced in text (and, transitively, in those skills) appended
// as delimited blocks. Tokens stay in place. It returns the text, the names used and the notes; unknown or
// capped names are explained in a note appended to the text so the model can see what was not expanded.
func Expand(ctx context.Context, q db.Querier, text string) (string, []string, []string, error) {
	names := References(text)
	if len(names) == 0 {
		return text, []string{}, []string{}, nil
	}
	chosen, unknown, skipped, err := Closure(ctx, q, names, ExpandMaxSkills, ExpandMaxChars)
	if err != nil {
		return "", nil, nil, err
	}
	notes := []string{}
	if len(unknown) > 0 {
		notes = append(notes, "Unknown skills (no such skill is defined): "+strings.Join(unknown, ", "))
	}
	if len(skipped) > 0 {
		notes = append(notes, fmt.Sprintf("Not expanded (at most %d skills / %d characters per message; use load_skill): %s",
			ExpandMaxSkills, ExpandMaxChars, strings.Join(skipped, ", ")))
	}
	parts := []string{text}
	used := make([]string, 0, len(chosen))
	for _, s := range chosen {
		parts = append(parts, Block(s))
		used = append(used, s.Name)
	}
	parts = append(parts, notes...)
	return strings.Join(parts, "\n\n"), used, notes, nil
}

// ExpandHook is Runner.ExpandSkills: Expand without the notes.
func ExpandHook(ctx context.Context, q db.Querier, text string) (string, []string, error) {
	out, used, _, err := Expand(ctx, q, text)
	return out, used, err
}
