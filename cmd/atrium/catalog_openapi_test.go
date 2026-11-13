package main

import (
	"bytes"
	"encoding/json"
	"errors"
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
		} `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Required []string `json:"required"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(first, &doc); err != nil {
		t.Fatal(err)
	}
	me := doc.Paths["/me"]["get"]
	if doc.OpenAPI != "3.1.0" || len(doc.Servers) != 1 || doc.Servers[0].URL != "/api/v1" || me.OperationID != "auth.identity" || me.Area != "settings" {
		t.Fatalf("openapi.json: version %q servers %+v /me %+v", doc.OpenAPI, doc.Servers, me)
	}
	if search, threads := doc.Paths["/search"]["get"].OperationID, doc.Paths["/threads"]["get"].OperationID; search != "assistant.search" || threads != "assistant.thread.list" || search == threads {
		t.Fatalf("search/thread operation ids collide or changed: %q / %q", search, threads)
	}
	for schema, field := range map[string]string{
		"AssistantmoduleapiSchedulePatch":   "schedule",
		"AssistantmoduleapiScheduleEnabled": "enabled",
		"AssistantmoduleapiSubagentEnabled": "enabled",
	} {
		if !slices.Contains(doc.Components.Schemas[schema].Required, field) {
			t.Errorf("%s does not require %s", schema, field)
		}
	}
	if slices.Contains(doc.Components.Schemas["AssistantmoduleapiMemoryWork"].Required, "queued") {
		t.Error("memory work incorrectly requires queued")
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
	if len(reach.Areas) != 22 || !settings || reach.TotalBound < 1 || reach.TotalBound > reach.TotalRequired {
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
