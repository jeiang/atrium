package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/core/coremodule"
)

func TestSettingsSchemaDocumentMatchesTheServer(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	s := startAPISite(t, dir, api)
	schema := strictJSON[coremodule.SettingsSchema](t, "/settings/schema", s.do(http.MethodGet, "/api/v1/settings/schema", s.token("owner"), nil))
	for i := range schema.Pages {
		for j := range schema.Pages[i].Fields {
			f := &schema.Pages[i].Fields[j]
			f.Value, f.Source, f.PinSource = nil, nil, ""
		}
	}
	want, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := filepath.Join("..", "..", "docs", "api", "settings-schema.json")
	if os.Getenv("ATRIUM_WRITE_SETTINGS_SCHEMA") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale: run ATRIUM_WRITE_SETTINGS_SCHEMA=1 just e2e TestSettingsSchemaDocumentMatchesTheServer", path)
	}
}
