package llmstub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/testutil/embedstub"
	"github.com/jeiang/atrium/internal/testutil/mailscript"
)

func Server(t *testing.T, inspect func(feature string, body []byte) bool) *httptest.Server {
	t.Helper()
	chat := mailscript.NewOpenAI(t, mailscript.Load(t), inspect)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			embedstub.Handler(w, r)
			return
		}
		chat.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}
