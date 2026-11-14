package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	s := newAPISite(t)
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
		ApprovalReached []reachApprovalRow `json:"approval_reached"`
		TotalRequired        int `json:"total_required"`
		TotalBound           int `json:"total_bound"`
		TotalApprovalReached int `json:"total_approval_reached"`
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

	var doc struct {
		Paths map[string]map[string]struct {
			Tags []string `json:"tags"`
		} `json:"paths"`
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
	for _, a := range application.Areas {
		p, ok := pick[string(a)]
		if !ok {
			t.Errorf("area %s has no GET path in the schema", a)
			continue
		}
		got := s.do(http.MethodGet, "/api/v1"+param.ReplaceAllString(p, "1"), token, nil)
		routeMissing := got.status == http.StatusNotFound && got.code() == "not_found" && !strings.Contains(p, "{")
		if got.status == http.StatusMethodNotAllowed || routeMissing || got.status == http.StatusUnauthorized {
			t.Errorf("area %s: GET %s = %d %.200s", a, p, got.status, got.body)
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
