package about

import (
	"html"
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
	body := html.UnescapeString(response.Body.String())
	for _, want := range []string{
		`class="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-accent bg-accent font-medium"`,
		`href="/about"`,
		`aria-current="page"`,
		"About",
		"Version:",
		"test-version",
		"Licence notices",
		"MIT License",
		"Developer Certificate of Origin",
		"<details>",
		"Third-party licences (0 packages)",
		`role="region" aria-label="Third-party licence notices"`,
		"max-h-[70vh] overflow-auto break-words whitespace-pre-wrap text-xs",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /about does not contain %q", want)
		}
	}
	if notices := strings.TrimSpace(thirdPartyNotices); notices != "" && !strings.Contains(body, notices) {
		t.Error("GET /about does not include the embedded dependency notices")
	}
}
