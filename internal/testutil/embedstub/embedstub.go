package embedstub

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/config"
)

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

func Handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/embeddings") {
		http.Error(w, "embedstub serves only /embeddings", http.StatusNotFound)
		return
	}
	var req struct {
		Input []string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	data := make([]any, len(req.Input))
	for i, text := range req.Input {
		data[i] = map[string]any{"index": i, "embedding": SparseEmbedding(text)}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func Server(t testing.TB) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(Handler))
	t.Cleanup(srv.Close)
	return srv
}
