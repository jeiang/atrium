package report

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/module"
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
		n, err := mail.MessageCount(ctx, q, id)
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

func Metadata() tool.Tool {
	return tool.Tool{
		Name: Name,
		Description: "Finish with a typed report. Pass an object holding every requested field; use null for a field you " +
			"could not determine. The report is validated against the run's spec and returns an error listing each bad field.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"object"}},` +
			`"required":["answer"],"additionalProperties":false}`),
		Effect:    tool.Read,
		Annotated: true,
		Channel:   tool.ChannelNone,
		Contexts:  tool.Kinds{tool.Sub, tool.Nightly},
	}
}

func Tool() tool.Tool {
	t := Metadata()
	t.Handler = handle
	return t
}

func Definition() module.ToolDefinition {
	return module.ToolDefinition{Tool: Metadata()}
}

func Schema(raw json.RawMessage) (json.RawMessage, error) {
	var spec Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("report: spec: %w", err)
	}
	properties := make(map[string]any, len(spec.Fields))
	required := make([]string, 0, len(spec.Fields))
	for _, field := range spec.Fields {
		properties[field.Name] = fieldSchema(field)
		required = append(required, field.Name)
	}
	return json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"answer": map[string]any{
				"type":                 "object",
				"properties":           properties,
				"required":             required,
				"additionalProperties": false,
			},
		},
		"required":             []string{"answer"},
		"additionalProperties": false,
	})
}

func fieldSchema(field Field) map[string]any {
	if field.List {
		item := field
		item.List = false
		limit := field.Max
		if limit <= 0 {
			limit = defaultListMax
		}
		return map[string]any{
			"type":     []string{"array", "null"},
			"items":    fieldSchema(item),
			"maxItems": limit,
		}
	}
	switch field.Type {
	case Number:
		return map[string]any{"type": []string{"number", "null"}}
	case Date:
		return map[string]any{"type": []string{"string", "null"}}
	case Boolean:
		return map[string]any{"type": []string{"boolean", "null"}}
	case Enum:
		schema := map[string]any{"type": []string{"string", "null"}}
		if len(field.Values) > 0 {
			schema["enum"] = field.Values
		}
		return schema
	case ID:
		schema := map[string]any{"type": []string{"integer", "null"}}
		if len(field.Within) > 0 {
			schema["enum"] = field.Within
		}
		return schema
	case Text:
		limit := field.Max
		if limit <= 0 {
			limit = defaultTextMax
		}
		return map[string]any{"type": []string{"string", "null"}, "maxLength": limit}
	case Object:
		properties := make(map[string]any, len(field.Fields))
		required := make([]string, 0, len(field.Fields))
		for _, sub := range field.Fields {
			properties[sub.Name] = fieldSchema(sub)
			required = append(required, sub.Name)
		}
		return map[string]any{
			"type":                 []string{"object", "null"},
			"properties":           properties,
			"required":             required,
			"additionalProperties": false,
		}
	default:
		return map[string]any{"type": []string{"string", "null"}}
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

func Last(ctx context.Context, q db.Querier, threadID int64) (map[string]any, bool, error) {
	var toolUseID string
	err := q.QueryRowContext(ctx,
		"SELECT c.tool_use_id FROM tool_calls c JOIN runs r ON r.id = c.run_id "+
			"WHERE r.thread_id = ? AND c.name = ? AND c.state = ? ORDER BY c.id DESC LIMIT 1",
		threadID, Name, journal.OK).Scan(&toolUseID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	messages, err := journal.MessagesOf(ctx, q, threadID)
	if err != nil {
		return nil, false, err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		var blocks []map[string]any
		if err := json.Unmarshal([]byte(jsonx.Dumps(messages[i].Content)), &blocks); err != nil {
			return nil, false, fmt.Errorf("report: thread %d message %d: %w", threadID, messages[i].ID, err)
		}
		for _, block := range blocks {
			if block["type"] != "tool_result" || block["tool_use_id"] != toolUseID || block["is_error"] == true {
				continue
			}
			content, _ := block["content"].(string)
			var stub struct {
				ResultID *int64 `json:"result_id"`
			}
			if json.Unmarshal([]byte(content), &stub) == nil && stub.ResultID != nil {
				if err := q.QueryRowContext(ctx, "SELECT body FROM tool_results WHERE result_id = ?", *stub.ResultID).Scan(&content); err != nil {
					return nil, false, err
				}
			}
			var answer map[string]any
			if err := json.Unmarshal([]byte(content), &answer); err != nil {
				return nil, false, fmt.Errorf("report: thread %d report: %w", threadID, err)
			}
			return answer, true, nil
		}
	}
	return nil, false, nil
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
