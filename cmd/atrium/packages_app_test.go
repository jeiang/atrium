package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
)

func TestPackagesAPIAddsTracksPollsUntracksAndDismisses(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	fake := &fakeShip24{milestone: "in_transit"}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)
	setEnv(t, "ATRIUM_HOST_CONFIG", ship24Host(t, dir, srv.URL+"/public/v1", "good"))
	token := localAPIToken(t, "packages")
	s := &apiSite{t: t, base: startServeIn(t, dir)}

	list := apiData(t, s.do(http.MethodGet, "/api/v1/packages", token, nil), http.StatusOK)
	quota, _ := list["quota"].(map[string]any)
	if len(apiItems(t, list)) != 0 || list["ship24_connected"] != true || quota["used"] != float64(0) || quota["limit"] != float64(10) || quota["remaining"] != float64(10) {
		t.Fatalf("empty package list: %v", list)
	}
	add := map[string]any{"tracking_number": "1Z999AA10123456784", "carrier": "UPS", "merchant": "Shop"}
	id := number(t, apiData(t, s.do(http.MethodPost, "/api/v1/packages", token, add), http.StatusOK)["package_id"])
	expectAPI(t, s.do(http.MethodPost, "/api/v2/packages", token, add), http.StatusConflict, "conflict")

	tracked := apiData(t, s.do(http.MethodPost, fmt.Sprintf("/api/v1/packages/%d/track", id), token, map[string]any{}), http.StatusOK)
	if raw, _ := json.Marshal(tracked["tracking"]); !strings.Contains(string(raw), "trk_1") || !strings.Contains(string(raw), "in_transit") {
		t.Fatalf("track result: %s", raw)
	}
	list = apiData(t, s.do(http.MethodGet, "/api/v2/packages?active=true", token, nil), http.StatusOK)
	items := apiItems(t, list)
	quota, _ = list["quota"].(map[string]any)
	if len(items) != 1 || packageField(items[0], "tracker_id") != "trk_1" || packageField(items[0], "tracking_number") != "1Z999AA10123456784" || quota["used"] != float64(1) {
		t.Fatalf("package list after tracking: %v", list)
	}
	if poll := apiData(t, s.do(http.MethodPost, "/api/v1/packages/poll", token, nil), http.StatusOK); poll["poll"] == nil {
		t.Fatalf("poll result: %v", poll)
	}

	untracked := apiData(t, s.do(http.MethodPost, fmt.Sprintf("/api/v1/packages/%d/untrack", id), token, nil), http.StatusOK)
	if untracked["tracker_id"] != "trk_1" || number(t, untracked["package_id"]) != id || fake.unsubscribeCount() != 1 {
		t.Fatalf("untrack: %v, %d unsubscribes", untracked, fake.unsubscribeCount())
	}
	if items := apiItems(t, apiData(t, s.do(http.MethodGet, "/api/v1/packages", token, nil), http.StatusOK)); len(items) != 1 || packageField(items[0], "tracker_id") != "" {
		t.Fatalf("package after untrack: %v", items)
	}
	expectAPI(t, s.do(http.MethodPost, fmt.Sprintf("/api/v1/packages/%d/untrack", id), token, nil), http.StatusConflict, "conflict")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/packages/999/track", token, map[string]any{}), http.StatusNotFound, "not_found")

	apiData(t, s.do(http.MethodPost, fmt.Sprintf("/api/v1/packages/%d/dismiss", id), token, nil), http.StatusOK)
	if items := apiItems(t, apiData(t, s.do(http.MethodGet, "/api/v1/packages?active=true", token, nil), http.StatusOK)); len(items) != 0 {
		t.Fatalf("active packages after dismissing: %v", items)
	}
	for _, old := range []string{"/api/v1/mail/packages/poll", "/api/v2/mail/packages/poll"} {
		if got := s.do(http.MethodPost, old, token, nil); got.status != http.StatusNotFound {
			t.Fatalf("retired %s answered %d %s", old, got.status, got.body)
		}
	}
}

func TestTurningPackagesOffHidesItsToolsAndKeepsTheShip24Row(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	fake := &fakeShip24{milestone: "in_transit"}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)
	setEnv(t, "ATRIUM_HOST_CONFIG", ship24Host(t, dir, srv.URL+"/public/v1", "good"))
	if ran := runApp(t, "connections", "test", "service/ship24"); ran.code != cli.ExitOK || connectionState(t, dir, "service/ship24") != "ok" {
		t.Fatalf("Ship24 Test: %d %s %s", ran.code, ran.stdout, ran.stderr)
	}
	packageTools := []string{"packages", "track_package", "untrack_package"}
	if names := offeredTools(t, api, runApp(t, "agent", "run", "--text", "hello", "--kind", "chat")); !slices.Contains(names, "untrack_package") || !slices.Contains(names, "packages") {
		t.Fatalf("package tools while Packages is on: %v", names)
	}
	if got := runSettings(t, "set", module.FlagKey(unit.Packages), "false"); got.code != cli.ExitOK {
		t.Fatalf("turn Packages off: exit %d %q", got.code, got.stderr)
	}
	if names := offeredTools(t, api, runApp(t, "agent", "run", "--text", "hello", "--kind", "chat")); slices.ContainsFunc(names, func(n string) bool { return slices.Contains(packageTools, n) }) {
		t.Fatalf("package tools offered while Packages is off: %v", names)
	}
	if ran := runApp(t, "packages", "poll"); ran.code != cli.ExitOK || ran.stdout != "" {
		t.Fatalf("packages poll while off: exit %d stdout %q stderr %q", ran.code, ran.stdout, ran.stderr)
	}
	if connectionState(t, dir, "service/ship24") == "" {
		t.Fatal("turning Packages off deleted the service/ship24 row")
	}
}

func packageField(item any, name string) any {
	m, _ := item.(map[string]any)
	return m[name]
}
