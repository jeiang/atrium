package main

import (
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var metricName = regexp.MustCompile(`\batrium_[a-z0-9_]+\b`)

func TestMonitoringPackReferencesOnlyServedMetrics(t *testing.T) {
	dir, _ := moduleFixture(t, flagsExcept("", ""))
	opsBase := opsAddr(t)
	startServeIn(t, dir)
	_, scrape, _ := fetch(t, http.DefaultClient, "GET", opsBase+"/metrics", nil)

	for _, path := range []string{"../../nix/monitoring/atrium.rules.yml", "../../nix/monitoring/atrium-dashboard.json"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		names := metricName.FindAllString(string(raw), -1)
		slices.Sort(names)
		for _, name := range slices.Compact(names) {
			if !strings.Contains(scrape, "# TYPE "+name+" ") {
				t.Errorf("%s references %s, which the ops listener does not serve", path, name)
			}
		}
	}
}
