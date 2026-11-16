package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func runCatalogOpenAPI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	path := os.Getenv(builtBinaryEnv)
	if path == "" {
		t.Skipf("catalog openapi runs the built binary; %s is set by the e2e runner", builtBinaryEnv)
	}
	cmd := exec.Command(path, append([]string{"catalog", "openapi"}, args...)...)
	cmd.Env = []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("catalog openapi %v: %v", args, err)
		}
		return exit.ExitCode(), stderr.String()
	}
	return 0, stderr.String()
}

func TestCatalogOpenAPIGeneratesAndChecks(t *testing.T) {
	dir := t.TempDir()
	if code, stderr := runCatalogOpenAPI(t, "--output", dir); code != 0 {
		t.Fatalf("catalog openapi --output exited %d: %s", code, stderr)
	}
	first, err := os.ReadFile(filepath.Join(dir, "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		OpenAPI string `json:"openapi"`
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Area        string `json:"x-atrium-area"`
			Servers     []struct {
				URL string `json:"url"`
			} `json:"servers"`
			Parameters []struct {
				Name string `json:"name"`
				In   string `json:"in"`
			} `json:"parameters"`
		} `json:"paths"`
		Security   []map[string][]string `json:"security"`
		Components struct {
			Schemas map[string]struct {
				Required             []string                   `json:"required"`
				AdditionalProperties *bool                      `json:"additionalProperties"`
				Properties           map[string]json.RawMessage `json:"properties"`
				AnyOf                []json.RawMessage          `json:"anyOf"`
			} `json:"schemas"`
			SecuritySchemes map[string]struct {
				Type   string `json:"type"`
				Scheme string `json:"scheme"`
			} `json:"securitySchemes"`
		} `json:"components"`
	}
	if err := json.Unmarshal(first, &doc); err != nil {
		t.Fatal(err)
	}
	me := doc.Paths["/me"]["get"]
	session := doc.Paths["/session"]["get"]
	if doc.OpenAPI != "3.1.0" || len(doc.Servers) != 2 || doc.Servers[0].URL != "/api/v1" || doc.Servers[1].URL != "/web/v1" || me.OperationID != "auth.identity" || me.Area != "settings" || len(me.Servers) != 0 {
		t.Fatalf("openapi.json: version %q servers %+v /me %+v", doc.OpenAPI, doc.Servers, me)
	}
	if logout := doc.Paths["/session/logout"]["post"]; session.OperationID != "web.session" || len(session.Servers) != 1 || session.Servers[0].URL != "/web/v1" || logout.OperationID != "web.session.logout" || len(logout.Servers) != 1 {
		t.Fatalf("web session routes: %+v %+v", session, logout)
	}
	if search, threads := doc.Paths["/search"]["get"].OperationID, doc.Paths["/threads"]["get"].OperationID; search != "assistant.search" || threads != "assistant.thread.list" || search == threads {
		t.Fatalf("search/thread operation ids collide or changed: %q / %q", search, threads)
	}
	for path, ops := range doc.Paths {
		for method, op := range ops {
			seen := map[string]bool{}
			for _, p := range op.Parameters {
				key := p.In + " " + p.Name
				if p.In == "header" {
					key = p.In + " " + http.CanonicalHeaderKey(p.Name)
				}
				if seen[key] {
					t.Errorf("%s %s declares parameter %s twice", method, path, key)
				}
				seen[key] = true
			}
		}
	}
	for schema, field := range map[string]string{
		"SchedulePatch":   "schedule",
		"ScheduleEnabled": "enabled",
	} {
		s := doc.Components.Schemas[schema]
		if !slices.Contains(s.Required, field) {
			t.Errorf("%s does not require %s", schema, field)
		}
		if s.AdditionalProperties == nil || *s.AdditionalProperties {
			t.Errorf("request schema %s accepts unknown fields", schema)
		}
	}
	if slices.Contains(doc.Components.Schemas["MemoryWork"].Required, "queued") {
		t.Error("memory work incorrectly requires queued")
	}
	if step := doc.Components.Schemas["Step"]; step.AdditionalProperties != nil || !strings.Contains(string(step.Properties["error"]), `"string"`) {
		t.Errorf("response schema Step is closed or leaves error untyped: %+v", step)
	}
	if bearer, cookie := doc.Components.SecuritySchemes["bearer"], doc.Components.SecuritySchemes["webSession"]; bearer.Type != "http" || bearer.Scheme != "bearer" || cookie.Type != "apiKey" || len(doc.Security) != 2 || doc.Security[0]["bearer"] == nil || doc.Security[1]["webSession"] == nil {
		t.Fatalf("security: %+v %+v", doc.Security, doc.Components.SecuritySchemes)
	}
	var envelope struct {
		Properties struct {
			Code struct {
				AnyOf []struct {
					Type string   `json:"type"`
					Enum []string `json:"enum"`
				} `json:"anyOf"`
			} `json:"code"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(doc.Components.Schemas["ErrorResponse"].Properties["error"], &envelope); err != nil {
		t.Fatal(err)
	}
	if open := envelope.Properties.Code.AnyOf; len(open) != 2 || !slices.Contains(open[0].Enum, "not_found") || open[1].Type != "string" || open[1].Enum != nil {
		t.Errorf("error code is not an open enum: %+v", open)
	}
	if events := doc.Components.Schemas["RunStreamEvent"]; len(events.AnyOf) != 2 || !strings.Contains(string(events.AnyOf[0]), "RunStreamEventToolDone") {
		t.Errorf("RunStreamEvent is not an open union of typed events: %+v", events)
	}
	if events := doc.Components.Schemas["AppEvent"]; len(events.AnyOf) != 2 || !strings.Contains(string(events.AnyOf[0]), "AppEventSync") || !strings.Contains(string(events.AnyOf[0]), "AppEventChange") {
		t.Errorf("AppEvent is not an open union of typed events: %+v", events)
	}
	var reach struct {
		Areas []struct {
			Area       string   `json:"area"`
			Operations []string `json:"operations"`
		} `json:"areas"`
		TotalRequired int `json:"total_required"`
		TotalBound    int `json:"total_bound"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "reach.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &reach); err != nil {
		t.Fatal(err)
	}
	settings := false
	for _, a := range reach.Areas {
		if a.Area == "settings" && strings.Contains(strings.Join(a.Operations, " "), "auth.identity") {
			settings = true
		}
	}
	if len(reach.Areas) != 20 || !settings || reach.TotalBound < 1 || reach.TotalBound > reach.TotalRequired {
		t.Fatalf("reach.json: %s", raw)
	}

	if code, stderr := runCatalogOpenAPI(t, "--output", dir); code != 0 {
		t.Fatalf("second catalog openapi --output exited %d: %s", code, stderr)
	}
	second, err := os.ReadFile(filepath.Join(dir, "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("two generations differ")
	}
	if code, stderr := runCatalogOpenAPI(t, "--check", dir); code != 0 {
		t.Fatalf("catalog openapi --check exited %d on fresh files: %s", code, stderr)
	}

	drift := t.TempDir()
	var edited map[string]any
	if err := json.Unmarshal(first, &edited); err != nil {
		t.Fatal(err)
	}
	delete(edited["paths"].(map[string]any), "/me")
	out, err := json.MarshalIndent(edited, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(drift, "openapi.json"), out, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(drift, "reach.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	code, stderr := runCatalogOpenAPI(t, "--check", drift)
	if code != 1 || !strings.Contains(stderr, "go run ./cmd/atrium catalog openapi --output "+drift) {
		t.Fatalf("catalog openapi --check on a drifted copy exited %d: %s", code, stderr)
	}
	if code, _ := runCatalogOpenAPI(t, "--check", t.TempDir()); code != 1 {
		t.Fatalf("catalog openapi --check on an empty directory exited %d", code)
	}
}
