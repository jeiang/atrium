package llmstub

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/mail/rebalance"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

type script struct {
	Defaults  map[string]json.RawMessage            `json:"defaults"`
	BySubject map[string]map[string]json.RawMessage `json:"by_subject"`
}

func subjectOf(prompt string) (string, bool) {
	for line := range strings.SplitSeq(prompt, "\n") {
		if subject, ok := strings.CutPrefix(line, "Subject: "); ok {
			return subject, true
		}
	}
	return "", false
}

func SparseEmbedding(text string) []float64 {
	v := make([]float64, config.EmbedDim)
	for _, token := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		sum := sha256.Sum256([]byte(token))
		v[binary.BigEndian.Uint32(sum[:4])%config.EmbedDim]++
	}
	return v
}

func reply(w http.ResponseWriter, content string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
}

func Server(t *testing.T, inspect func(feature string, body []byte) bool) *httptest.Server {
	t.Helper()
	var s script
	if err := json.Unmarshal(golden.ReadFile(t, "pipeline", "llm_script.json"), &s); err != nil {
		t.Fatal(err)
	}
	features := map[string]string{
		llm.FolderSystem: "folder", llm.FlagSystem: "flag", llm.EventSystem: "event",
		openloops.CommitmentSystem: "commitments", watches.PackageSystem: "package", rebalance.NameSystem: "folder_name",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input    []string `json:"input"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			data := make([]any, len(req.Input))
			for i, text := range req.Input {
				data[i] = map[string]any{"index": i, "embedding": SparseEmbedding(text)}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			return
		}
		feature, known := features[req.Messages[0].Content]
		if !known {
			http.Error(w, "unknown system prompt", http.StatusBadRequest)
			return
		}
		if inspect != nil && !inspect(feature, body) {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		answer := s.Defaults[feature]
		if subject, found := subjectOf(req.Messages[1].Content); found {
			if override, ok := s.BySubject[subject][feature]; ok && feature != "folder_name" {
				merged := map[string]json.RawMessage{}
				_ = json.Unmarshal(answer, &merged)
				over := map[string]json.RawMessage{}
				_ = json.Unmarshal(override, &over)
				for k, v := range over {
					merged[k] = v
				}
				answer, _ = json.Marshal(merged)
			}
		}
		reply(w, string(answer))
	}))
	t.Cleanup(srv.Close)
	return srv
}
