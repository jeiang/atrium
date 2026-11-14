package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/cli"
)

type reachApprovalRow struct {
	Operation  string `json:"operation"`
	ReachedVia string `json:"reached_via"`
}

func TestEveryAreaReachableFromSchema(t *testing.T) {
	dir, _ := davFixture(t)
	if got := runApp(t, "calendar", "sync"); got.code != cli.ExitOK {
		t.Fatalf("sync reach fixture calendar: %d %s", got.code, got.stderr)
	}
	s := startAPISite(t, dir, nil)
	out := t.TempDir()
	if got := runApp(t, "catalog", "openapi", "--output", out); got.code != cli.ExitOK {
		t.Fatalf("catalog openapi: %d %s", got.code, got.stderr)
	}
	if got := runApp(t, "catalog", "operations", "--require-api"); got.code != cli.ExitOK {
		t.Fatalf("catalog operations --require-api: %d %s %s", got.code, got.stdout, got.stderr)
	}
	var reach struct {
		Areas []struct {
			Area       string   `json:"area"`
			Operations []string `json:"operations"`
		} `json:"areas"`
		ApprovalReached      []reachApprovalRow `json:"approval_reached"`
		TotalRequired        int                `json:"total_required"`
		TotalBound           int                `json:"total_bound"`
		TotalApprovalReached int                `json:"total_approval_reached"`
	}
	readJSON(t, filepath.Join(out, "reach.json"), &reach)
	if len(application.Areas) != 22 || len(reach.Areas) != len(application.Areas) {
		t.Fatalf("reach lists %d areas, the catalog defines %d, want 22", len(reach.Areas), len(application.Areas))
	}
	for _, a := range reach.Areas {
		if len(a.Operations) == 0 {
			t.Errorf("area %s has no operation", a.Area)
		}
	}
	if reach.TotalRequired == 0 || reach.TotalBound != reach.TotalRequired {
		t.Fatalf("reach bound %d of %d required operations", reach.TotalBound, reach.TotalRequired)
	}
	if reach.TotalApprovalReached != len(reach.ApprovalReached) || !slices.Contains(reach.ApprovalReached, reachApprovalRow{Operation: "calendar.request-delete", ReachedVia: "review.approve"}) {
		t.Fatalf("approval-reached operations = %+v (%d)", reach.ApprovalReached, reach.TotalApprovalReached)
	}

	type responseSchema struct {
		Ref        string                     `json:"$ref"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Tags      []string `json:"tags"`
			Responses map[string]struct {
				Content map[string]struct {
					Schema responseSchema `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]responseSchema `json:"schemas"`
		} `json:"components"`
	}
	readJSON(t, filepath.Join(out, "openapi.json"), &doc)
	param := regexp.MustCompile(`\{[^}]+\}`)
	pick := map[string]string{}
	paths := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		get, ok := doc.Paths[p]["get"]
		if !ok || len(get.Tags) == 0 {
			continue
		}
		area := get.Tags[0]
		if have, ok := pick[area]; !ok || strings.Contains(have, "{") && !strings.Contains(p, "{") {
			pick[area] = p
		}
	}
	token := s.token("owner")
	project := s.do(http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Reach fixture", "effort": "high", "context_limit": 200000})
	if project.status != http.StatusCreated {
		t.Fatalf("create reach fixture project: %d %s", project.status, project.body)
	}
	projectID := strconv.FormatInt(number(t, project.json["id"]), 10)
	for _, a := range application.Areas {
		p, ok := pick[string(a)]
		if !ok {
			t.Errorf("area %s has no GET path in the schema", a)
			continue
		}
		path := "/api/v1" + param.ReplaceAllString(p, projectID)
		if string(a) == "search" {
			path += "?q=reach"
		}
		got := s.do(http.MethodGet, path, token, nil)
		if got.status != http.StatusOK {
			t.Errorf("area %s: GET %s = %d %.200s", a, p, got.status, got.body)
			continue
		}
		schema := doc.Paths[p]["get"].Responses["200"].Content["application/json"].Schema
		if schema.Ref != "" {
			schema = doc.Components.Schemas[strings.TrimPrefix(schema.Ref, "#/components/schemas/")]
		}
		keys := make([]string, 0, len(schema.Properties))
		for key := range schema.Properties {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		if len(keys) == 0 {
			t.Errorf("area %s: GET %s has no object field in its schema", a, p)
			continue
		}
		if _, ok := got.json[keys[0]]; !ok {
			t.Errorf("area %s: GET %s lacks %q: %.200s", a, p, keys[0], got.body)
		}
	}
}

func TestApprovalOnlyOperationRunsOnceThroughTheApprovalRoute(t *testing.T) {
	dir, srv := davFixture(t)
	created := proposalID(t, toolResult(t, "calendar_create", map[string]any{
		"calendar": "icloud", "title": "Dentist", "start": "2026-11-10T12:00:00", "end": "2026-11-10T13:00:00",
	}))
	if got := runApp(t, "proposals", "approve", created); got.code != cli.ExitOK {
		t.Fatalf("approve create: %d %q %q", got.code, got.stdout, got.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	var uid string
	if err := d.Read.QueryRow("SELECT uid FROM agent_events WHERE deleted_ts IS NULL").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	removed := proposalID(t, toolResult(t, "calendar_delete", map[string]any{"uid": uid}))
	if len(srv.Objects()) != 1 {
		t.Fatalf("proposing the delete changed the calendar: %v", srv.Objects())
	}
	s := startAPISite(t, dir, nil)
	token := s.token("owner")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/approvals/"+removed+"/approve", "", nil), http.StatusUnauthorized, "unauthenticated")
	first := s.do(http.MethodPost, "/api/v1/approvals/"+removed+"/approve", token, nil)
	if first.status != http.StatusOK || first.json["outcome"] == "failed" {
		t.Fatalf("approve delete over the API = %d %s", first.status, first.body)
	}
	if len(srv.Objects()) != 0 {
		t.Fatalf("the approved delete left %v", srv.Objects())
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/approvals/"+removed+"/approve", token, nil), http.StatusConflict, "not_approved")
	var effects, audits int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM effects WHERE operation = 'calendar_delete' AND direction = 'forward'").Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE operation = 'calendar.request-delete' AND actor = 'operator' AND source = 'api'").Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if effects != 1 || audits != 1 {
		t.Fatalf("%d calendar_delete effects and %d API audits, want 1 and 1", effects, audits)
	}
}

func readJSON(t *testing.T, path string, into any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
