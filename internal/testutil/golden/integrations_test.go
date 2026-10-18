package golden

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var integrationSlices = []string{"cbtt", "eztv", "grafana", "llm", "notion", "ntfy", "qbittorrent", "tmdb"}

var encodings = []string{"json", "form", "multipart", "text", "base64", "none"}

func TestIntegrationGoldensLoad(t *testing.T) {
	for _, slice := range integrationSlices {
		t.Run(slice, func(t *testing.T) {
			m := LoadManifest(t, slice)
			if m.Slice != slice || len(m.FreezeCommit) != 40 || len(m.Scenarios) == 0 {
				t.Fatalf("manifest: %+v", m)
			}
			listed := map[string]bool{"manifest.json": true, "README.md": true}
			for _, entry := range m.Scenarios {
				listed[entry.File] = true
				s := LoadScenario(t, slice, entry.Name)
				if s.FreezeCommit != m.FreezeCommit || len(s.Exchanges) != entry.Requests || len(s.Exchanges) == 0 || len(entry.SourceTests) == 0 {
					t.Errorf("%s: freeze %s, %d exchanges, manifest says %d", entry.Name, s.FreezeCommit, len(s.Exchanges), entry.Requests)
				}
				for i, x := range s.Exchanges {
					checkRequest(t, entry.Name, i, x.Request)
				}
			}
			files, err := os.ReadDir(filepath.Join(Dir(), slice))
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range files {
				if !listed[f.Name()] {
					t.Errorf("%s is not in the manifest", f.Name())
				}
			}
		})
	}
}

func checkRequest(t *testing.T, scenario string, i int, r Request) {
	t.Helper()
	if r.Method == "" || !strings.HasSuffix(r.URL, r.Path) || !strings.HasPrefix(r.Path, "/") || !slices.Contains(encodings, r.BodyEncoding) {
		t.Errorf("%s[%d]: %s %s (%s) encoding %q", scenario, i, r.Method, r.URL, r.Path, r.BodyEncoding)
	}
	if (r.BodyEncoding == "none") != (string(r.Body) == "null") {
		t.Errorf("%s[%d]: encoding %s with body %s", scenario, i, r.BodyEncoding, r.Body)
	}
	if r.BodyEncoding == "json" {
		canonical, err := Canonical(r.Body)
		if err != nil {
			t.Fatalf("%s[%d]: %v", scenario, i, err)
		}
		EqualJSON(t, r.Body, canonical)
	}
	if r.BodyEncoding == "form" {
		var pairs [][2]string
		if err := json.Unmarshal(r.Body, &pairs); err != nil {
			t.Errorf("%s[%d]: form body: %v", scenario, i, err)
		}
	}
	if auth, ok := r.Headers["authorization"]; ok && !strings.HasPrefix(auth, "Bearer golden-") {
		t.Errorf("%s[%d]: authorization %q is not a golden fake", scenario, i, auth)
	}
}
