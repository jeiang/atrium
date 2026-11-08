package files

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
)

type citations struct {
	Enabled bool `json:"enabled"`
}

type textSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type documentBlock struct {
	Type         string          `json:"type"`
	Source       textSource      `json:"source"`
	Title        string          `json:"title"`
	Context      string          `json:"context"`
	Citations    citations       `json:"citations"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

type imageBlock struct {
	Type         string          `json:"type"`
	Source       textSource      `json:"source"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

type textBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func marshal(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func decodeBlock(raw json.RawMessage) (map[string]any, error) {
	v, err := jsonx.Loads(raw)
	if err != nil {
		return nil, fmt.Errorf("files: content block: %w", err)
	}
	d, _ := v.(map[string]any)
	return d, nil
}

func isAttachment(d map[string]any) bool {
	t := d["type"]
	return t == Attachment
}

func fileIDOf(d map[string]any) (int64, error) {
	v, ok := d["file_id"]
	id, isInt := v.(int64)
	if !ok || !isInt {
		return 0, errors.New("files: attachment block without a file_id")
	}
	return id, nil
}

func unavailable(d map[string]any) (json.RawMessage, error) {
	name := "?"
	if v, ok := d["name"]; ok {
		if s, isString := v.(string); isString {
			name = s
		} else {
			name = fmt.Sprint(v)
		}
	}
	return marshal(textBlock{Type: "text", Text: "[attachment " + name + " is no longer available]"})
}

type Scope struct {
	thread  int64
	project *int64
}

func ScopeOf(threadID int64, projectID *int64) Scope {
	return Scope{thread: threadID, project: projectID}
}

func (s Scope) owns(r *Row) bool {
	if r.ThreadID != nil && *r.ThreadID == s.thread {
		return true
	}
	return s.project != nil && r.ProjectID != nil && *r.ProjectID == *s.project
}

func apiBlocks(ctx context.Context, q db.Querier, scope Scope, d map[string]any) ([]json.RawMessage, error) {
	id, err := fileIDOf(d)
	if err != nil {
		return nil, err
	}
	found, err := Get(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if found == nil || !scope.owns(found) {
		b, err := unavailable(d)
		return []json.RawMessage{b}, err
	}
	var cache json.RawMessage
	if v, ok := d["cache_control"]; ok {
		cache = json.RawMessage(jsonx.Dumps(v))
	}
	var block any
	if found.Text != nil {
		block = documentBlock{
			Type: "document", Source: textSource{Type: "text", MediaType: "text/plain", Data: *found.Text},
			Title: found.Name, Context: documentContext, Citations: citations{Enabled: true}, CacheControl: cache,
		}
	} else {
		raw, err := os.ReadFile(found.Path)
		if errors.Is(err, os.ErrNotExist) {
			b, err := unavailable(d)
			return []json.RawMessage{b}, err
		}
		if err != nil {
			return nil, err
		}
		source := textSource{Type: "base64", MediaType: found.MediaType, Data: base64.StdEncoding.EncodeToString(raw)}
		if found.MediaType == PDFType {
			block = documentBlock{Type: "document", Source: source, Title: found.Name, Context: documentContext, Citations: citations{Enabled: true}, CacheControl: cache}
		} else {
			block = imageBlock{Type: "image", Source: source, CacheControl: cache}
		}
	}
	b, err := marshal(block)
	return []json.RawMessage{b}, err
}

func Expand(ctx context.Context, q db.Querier, scope Scope, content []json.RawMessage) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(content))
	for _, raw := range content {
		d, err := decodeBlock(raw)
		if err != nil {
			return nil, err
		}
		if !isAttachment(d) {
			out = append(out, raw)
			continue
		}
		blocks, err := apiBlocks(ctx, q, scope, d)
		if err != nil {
			return nil, err
		}
		out = append(out, blocks...)
	}
	return out, nil
}

func CloneContent(ctx context.Context, q db.Querier, from Scope, content []json.RawMessage, threadID int64, now float64) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(content))
	for _, raw := range content {
		d, err := decodeBlock(raw)
		if err != nil {
			return nil, err
		}
		if !isAttachment(d) {
			out = append(out, raw)
			continue
		}
		id, err := fileIDOf(d)
		if err != nil {
			return nil, err
		}
		copied, ok, err := clone(ctx, q, from, id, threadID, now)
		if err != nil {
			return nil, err
		}
		if !ok {
			out = append(out, raw)
			continue
		}
		d["file_id"] = copied
		out = append(out, json.RawMessage(jsonx.Dumps(d)))
	}
	return out, nil
}

func HiddenTokens(content []json.RawMessage) (int64, error) {
	var total int64
	for _, raw := range content {
		d, err := decodeBlock(raw)
		if err != nil {
			return 0, err
		}
		if !isAttachment(d) {
			continue
		}
		switch n := d["tokens"].(type) {
		case int64:
			total += n
		case float64:
			total += int64(n)
		}
	}
	return total, nil
}

func ProjectBlocks(ctx context.Context, q db.Querier, projectID int64) ([]json.RawMessage, error) {
	rows, err := OfProject(ctx, q, projectID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(rows)+1)
	for _, r := range rows {
		b, err := marshal(newAttachment(r.ID, r.Name, r.MediaType, kindOf(r.MediaType), r.Tokens))
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	b, err := marshal(textBlock{Type: "text", Text: projectNotice})
	if err != nil {
		return nil, err
	}
	return append(out, b), nil
}
