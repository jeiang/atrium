package about

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func TestAboutPage(t *testing.T) {
	render, err := web.NewRenderer(config.Config{}, nil, tpl.Config{Modules: []fs.FS{Templates()}})
	if err != nil {
		t.Fatal(err)
	}
	site, err := web.New(web.Options{Auth: web.AuthLocal, Render: render, Modules: []web.Module{Module("test-version")}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/about", nil)
	request.Host = "localhost"
	response := httptest.NewRecorder()
	site.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /about = %d: %s", response.Code, response.Body.String())
	}
	for _, want := range []string{"test-version", "Licence notices", "MIT License", "Developer Certificate of Origin"} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("GET /about does not contain %q", want)
		}
	}
	if notices := strings.TrimSpace(string(mustNotices())); notices != "" && !strings.Contains(response.Body.String(), notices) {
		t.Error("GET /about does not include the embedded dependency notices")
	}
}
