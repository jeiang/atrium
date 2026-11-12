package mailscript

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/testutil/golden"
)

type Script struct {
	defaults  map[string]json.RawMessage
	bySubject map[string]map[string]json.RawMessage
	shapes    map[string]string
}

func Load(t testing.TB) *Script {
	t.Helper()
	var raw struct {
		Defaults  map[string]json.RawMessage            `json:"defaults"`
		BySubject map[string]map[string]json.RawMessage `json:"by_subject"`
	}
	if err := json.Unmarshal(golden.ReadFile(t, "pipeline", "llm_script.json"), &raw); err != nil {
		t.Fatal(err)
	}
	s := &Script{defaults: raw.Defaults, bySubject: raw.BySubject, shapes: map[string]string{}}
	for feature, answer := range raw.Defaults {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(answer, &fields); err != nil {
			t.Fatalf("mailscript: default %s: %v", feature, err)
		}
		key := shape(slices.Collect(maps.Keys(fields)))
		if other, dup := s.shapes[key]; dup {
			t.Fatalf("mailscript: defaults %s and %s share key structure %s", other, feature, key)
		}
		s.shapes[key] = feature
	}
	return s
}

func shape(keys []string) string {
	keys = slices.Clone(keys)
	slices.Sort(keys)
	return strings.Join(keys, ",")
}

func subjectOf(prompt string) (string, bool) {
	for line := range strings.SplitSeq(prompt, "\n") {
		if subject, ok := strings.CutPrefix(line, "Subject: "); ok {
			return subject, true
		}
	}
	return "", false
}

func (s *Script) Answer(required []string, user string) (string, json.RawMessage, error) {
	feature, ok := s.shapes[shape(required)]
	if !ok {
		return "", nil, fmt.Errorf("mailscript: no scripted answer has key structure %v", required)
	}
	answer := s.defaults[feature]
	if subject, found := subjectOf(user); found {
		if override, ok := s.bySubject[subject][feature]; ok && feature != "folder_name" {
			merged := map[string]json.RawMessage{}
			if err := json.Unmarshal(answer, &merged); err != nil {
				return "", nil, err
			}
			over := map[string]json.RawMessage{}
			if err := json.Unmarshal(override, &over); err != nil {
				return "", nil, err
			}
			maps.Copy(merged, over)
			out, err := json.Marshal(merged)
			if err != nil {
				return "", nil, err
			}
			answer = out
		}
	}
	return feature, answer, nil
}

func (s *Script) JSONAnswer(required []string, user string) (json.RawMessage, error) {
	_, answer, err := s.Answer(required, user)
	return answer, err
}

type OpenAI struct {
	t       testing.TB
	script  *Script
	inspect func(feature string, body []byte) bool
}

func NewOpenAI(t testing.TB, script *Script, inspect func(feature string, body []byte) bool) *OpenAI {
	return &OpenAI{t: t, script: script, inspect: inspect}
}

func (o *OpenAI) Serve() *httptest.Server {
	o.t.Helper()
	srv := httptest.NewServer(o)
	o.t.Cleanup(srv.Close)
	return srv
}

func (o *OpenAI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		o.t.Errorf("mailscript: unexpected %s %s", r.Method, r.URL.Path)
		http.Error(w, "mailscript serves only /chat/completions", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		ResponseFormat struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Schema struct {
					Required []string `json:"required"`
				} `json:"schema"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		o.t.Errorf("mailscript: request body: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" || req.ResponseFormat.Type != "json_schema" {
		o.t.Errorf("mailscript: request is not one system and one user message with a JSON schema: %s", body)
		http.Error(w, "unknown request shape", http.StatusBadRequest)
		return
	}
	feature, answer, err := o.script.Answer(req.ResponseFormat.JSONSchema.Schema.Required, req.Messages[1].Content)
	if err != nil {
		o.t.Errorf("%v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if o.inspect != nil && !o.inspect(feature, body) {
		http.Error(w, "unexpected", http.StatusBadRequest)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(answer)}}}})
}
