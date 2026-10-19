package files

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/jeiang/atrium/internal/py"
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

func decodeBlock(raw json.RawMessage) (py.Dict, error) {
	v, err := py.JSONLoads(raw)
	if err != nil {
		return nil, fmt.Errorf("files: content block: %w", err)
	}
	d, _ := v.(py.Dict)
	return d, nil
}

func isAttachment(d py.Dict) bool {
	t, _ := d.Get("type")
	return t == Attachment
}

func fileIDOf(d py.Dict) (int64, error) {
	v, ok := d.Get("file_id")
	id, isInt := v.(int64)
	if !ok || !isInt {
		return 0, errors.New("files: attachment block without a file_id")
	}
	return id, nil
}

func unavailable(d py.Dict) (json.RawMessage, error) {
	name := "?"
	if v, ok := d.Get("name"); ok {
		if s, isString := v.(string); isString {
			name = s
		} else {
			name = py.ReprValue(v)
		}
	}
	return marshal(textBlock{Type: "text", Text: "[attachment " + name + " is no longer available]"})
}

func apiBlocks(ctx context.Context, q querier, d py.Dict) ([]json.RawMessage, error) {
	id, err := fileIDOf(d)
	if err != nil {
		return nil, err
	}
	found, err := Get(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if found == nil {
		b, err := unavailable(d)
		return []json.RawMessage{b}, err
	}
	var cache json.RawMessage
	if v, ok := d.Get("cache_control"); ok {
		cache = json.RawMessage(py.JSONDumps(v))
	}
	var block any
	if found.Text != nil {
		block = documentBlock{
			Type: "document", Source: textSource{Type: "text", MediaType: "text/plain", Data: *found.Text},
			Title: found.Name, Context: DocumentContext, Citations: citations{Enabled: true}, CacheControl: cache,
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
			block = documentBlock{Type: "document", Source: source, Title: found.Name, Context: DocumentContext, Citations: citations{Enabled: true}, CacheControl: cache}
		} else {
			block = imageBlock{Type: "image", Source: source, CacheControl: cache}
		}
	}
	b, err := marshal(block)
	return []json.RawMessage{b}, err
}

// APIBlocks is api_blocks: the content blocks one journaled attachment stands for, a document (a PDF or the
// extracted text) or an image, or a note that the file is gone.
func APIBlocks(ctx context.Context, q querier, block json.RawMessage) ([]json.RawMessage, error) {
	d, err := decodeBlock(block)
	if err != nil {
		return nil, err
	}
	return apiBlocks(ctx, q, d)
}

// Expand is expand for one message's content: attachment blocks become their request blocks, every other block
// stays as it is.
func Expand(ctx context.Context, q querier, content []json.RawMessage) ([]json.RawMessage, error) {
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
		blocks, err := apiBlocks(ctx, q, d)
		if err != nil {
			return nil, err
		}
		out = append(out, blocks...)
	}
	return out, nil
}

// CloneContent is clone_content: a branch's copy of each attachment in content, as new rows and bytes under the
// new thread. An attachment whose file is gone stays as it is.
func CloneContent(ctx context.Context, q querier, content []json.RawMessage, threadID int64, now float64) ([]json.RawMessage, error) {
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
		copied, ok, err := clone(ctx, q, id, threadID, now)
		if err != nil {
			return nil, err
		}
		if !ok {
			out = append(out, raw)
			continue
		}
		out = append(out, json.RawMessage(py.JSONDumps(d.Set("file_id", copied))))
	}
	return out, nil
}

// HiddenTokens is hidden_tokens: the estimate of the attachments in content, which the journal holds as stubs.
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
		switch v, _ := d.Get("tokens"); n := v.(type) {
		case int64:
			total += n
		case float64:
			total += int64(n)
		}
	}
	return total, nil
}

// ProjectBlocks is project_message: the blocks that open every request of a project's threads, one attachment per
// project file and a notice that they are untrusted data. Nil when the project has no files.
func ProjectBlocks(ctx context.Context, q querier, projectID int64) ([]json.RawMessage, error) {
	rows, err := OfProject(ctx, q, projectID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(rows)+1)
	for _, r := range rows {
		b, err := marshal(newAttachment(r.ID, r.Name, r.MediaType, KindOf(r.MediaType), r.Tokens))
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	b, err := marshal(textBlock{Type: "text", Text: ProjectNotice})
	if err != nil {
		return nil, err
	}
	return append(out, b), nil
}
