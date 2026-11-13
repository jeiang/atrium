package mailscript

import (
	"encoding/json"
	"fmt"
	"maps"
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
