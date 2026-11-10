package report

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/tool"
)

const Name = "report"

type Type string

const (
	Number  Type = "number"
	Date    Type = "date"
	Boolean Type = "boolean"
	Enum    Type = "enum"
	ID      Type = "id"
	Text    Type = "text"
	Object  Type = "object"
)

type Field struct {
	Name   string   `json:"name"`
	Type   Type     `json:"type"`
	Values []string `json:"values,omitempty"`
	IDOf   string   `json:"id_of,omitempty"`
	Within []int64  `json:"within,omitempty"`
	List   bool     `json:"list,omitempty"`
	Max    int      `json:"max,omitempty"`
	Fields []Field  `json:"fields,omitempty"`
}

type Spec struct {
	Fields []Field `json:"fields"`
}

const (
	defaultTextMax = 500
	defaultListMax = 20
)

var idKinds = []string{"mail_message", "file", "kb_record", "thread"}

func (s Spec) Validate(ctx context.Context, q db.Querier, project int64, now float64, answer map[string]any) (map[string]any, error) {
	known := make(map[string]struct{}, len(s.Fields))
	for _, f := range s.Fields {
		known[f.Name] = struct{}{}
	}
	var problems []string
	for name := range answer {
		if _, ok := known[name]; !ok {
			problems = append(problems, fmt.Sprintf("field %q is not in the report spec", name))
		}
	}
	out := make(map[string]any, len(s.Fields))
	for _, f := range s.Fields {
		value, ok := answer[f.Name]
		if !ok {
			problems = append(problems, fmt.Sprintf("field %q is missing; use null when unknown", f.Name))
			continue
		}
		clean, err := validateField(ctx, q, project, now, f, value)
		if err != nil {
			problems = append(problems, fmt.Sprintf("field %q: %v", f.Name, err))
			continue
		}
		out[f.Name] = clean
	}
	if len(problems) > 0 {
		return nil, tool.Error("report: " + strings.Join(problems, "; "))
	}
	return out, nil
}

func validateField(ctx context.Context, q db.Querier, project int64, now float64, f Field, value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	if f.List {
		list, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("must be a list")
		}
		limit := f.Max
		if limit <= 0 {
			limit = defaultListMax
		}
		if len(list) > limit {
			return nil, fmt.Errorf("list is longer than %d items", limit)
		}
		out := make([]any, 0, len(list))
		for i, item := range list {
			clean, err := validateSingle(ctx, q, project, now, f, item)
			if err != nil {
				return nil, fmt.Errorf("item %d: %v", i, err)
			}
			out = append(out, clean)
		}
		return out, nil
	}
	return validateSingle(ctx, q, project, now, f, value)
}

func validateSingle(ctx context.Context, q db.Querier, project int64, now float64, f Field, value any) (any, error) {
	switch f.Type {
	case Number:
		n, ok := number(value)
		if !ok || math.IsInf(n, 0) || math.IsNaN(n) {
			return nil, fmt.Errorf("must be a finite number")
		}
		return value, nil
	case Date:
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("must be a date string")
		}
		if _, err := time.Parse("2006-01-02", text); err == nil {
			return text, nil
		}
		if _, err := time.Parse(time.RFC3339, text); err == nil {
			return text, nil
		}
		return nil, fmt.Errorf("must be YYYY-MM-DD or RFC 3339")
	case Boolean:
		b, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("must be true or false")
		}
		return b, nil
	case Enum:
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("must be one of %s", strings.Join(f.Values, ", "))
		}
		if !slices.Contains(f.Values, text) {
			return nil, fmt.Errorf("must be one of %s", strings.Join(f.Values, ", "))
		}
		return text, nil
	case ID:
		id, ok := integer(value)
		if !ok {
			return nil, fmt.Errorf("must be an integer id")
		}
		found, err := idExists(ctx, q, project, now, f, id)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("no %s with id %d", f.IDOf, id)
		}
		return id, nil
	case Text:
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("must be a string")
		}
		limit := f.Max
		if limit <= 0 {
			limit = defaultTextMax
		}
		if utf8.RuneCountInString(text) > limit {
			return nil, fmt.Errorf("is longer than %d characters", limit)
		}
		return text, nil
	case Object:
		fields, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("must be an object")
		}
		known := make(map[string]struct{}, len(f.Fields))
		for _, sub := range f.Fields {
			known[sub.Name] = struct{}{}
		}
		var problems []string
		for name := range fields {
			if _, ok := known[name]; !ok {
				problems = append(problems, fmt.Sprintf("field %q is not in the report spec", name))
			}
		}
		out := make(map[string]any, len(f.Fields))
		for _, sub := range f.Fields {
			if sub.Type == Object {
				problems = append(problems, fmt.Sprintf("field %q: objects nest only one level", sub.Name))
				continue
			}
			value, ok := fields[sub.Name]
			if !ok {
				problems = append(problems, fmt.Sprintf("field %q is missing; use null when unknown", sub.Name))
				continue
			}
			clean, err := validateField(ctx, q, project, now, sub, value)
			if err != nil {
				problems = append(problems, fmt.Sprintf("field %q: %v", sub.Name, err))
				continue
			}
			out[sub.Name] = clean
		}
		if len(problems) > 0 {
			return nil, fmt.Errorf("%s", strings.Join(problems, "; "))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("has unknown type %q", f.Type)
	}
}

func number(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

func integer(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case json.Number:
		i, err := v.Int64()
		return i, err == nil
	case float64:
		if v == math.Trunc(v) && v >= math.MinInt64 && v <= math.MaxInt64 {
			return int64(v), true
		}
	}
	return 0, false
}

func idExists(ctx context.Context, q db.Querier, project int64, now float64, f Field, id int64) (bool, error) {
	if len(f.Within) > 0 && !slices.Contains(f.Within, id) {
		return false, nil
	}
	switch f.IDOf {
	case "mail_message":
		var n int
		err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE id = ?", id).Scan(&n)
		return n > 0, err
	case "file":
		var n int
		err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM thread_files WHERE id = ?", id).Scan(&n)
		return n > 0, err
	case "kb_record":
		found, err := kb.Read(ctx, q, kb.ReadFilter{IDs: []int64{id}, ProjectID: project, Now: now})
		return len(found) > 0, err
	case "thread":
		var n int
		err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM threads WHERE id = ? AND deleted_at IS NULL", id).Scan(&n)
		return n > 0, err
	default:
		return false, fmt.Errorf("has unknown id_of %q", f.IDOf)
	}
}

func Tool() tool.Tool {
	return tool.Tool{
		Name: Name,
		Description: "Finish with a typed report. Pass an object holding every requested field; use null for a field you " +
			"could not determine. The report is validated against the run's spec and returns an error listing each bad field.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"object"}},` +
			`"required":["answer"],"additionalProperties":false}`),
		Handler:   handle,
		Effect:    tool.Read,
		Annotated: true,
		Channel:   tool.ChannelNone,
		Contexts:  tool.Kinds{tool.Sub, tool.Nightly},
	}
}

func handle(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	answer, ok := args["answer"].(map[string]any)
	if !ok {
		return nil, tool.Error("answer must be an object")
	}
	limits, err := journal.LimitsOf(ctx, tc.DB.Read, tc.RunID)
	if err != nil {
		return nil, err
	}
	if len(limits.Report) == 0 {
		return nil, tool.Error("this run has no report spec")
	}
	var spec Spec
	if err := json.Unmarshal(limits.Report, &spec); err != nil {
		return nil, fmt.Errorf("report: run %d spec: %w", tc.RunID, err)
	}
	project, err := projectOf(ctx, tc.DB.Read, tc.ThreadID)
	if err != nil {
		return nil, err
	}
	now := tc.Now
	if now == nil {
		now = assistant.Now
	}
	return spec.Validate(ctx, tc.DB.Read, project, now(), answer)
}

func projectOf(ctx context.Context, q db.Querier, threadID int64) (int64, error) {
	var project *int64
	if err := q.QueryRowContext(ctx, "SELECT project_id FROM threads WHERE id = ?", threadID).Scan(&project); err != nil {
		return 0, err
	}
	if project == nil {
		return 0, nil
	}
	return *project, nil
}
