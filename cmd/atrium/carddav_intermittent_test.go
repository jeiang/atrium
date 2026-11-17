package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/davmirror"
)

func (p *partitionedDAV) drain() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.requests
	p.requests = nil
	return out
}

func capabilityResult(view icloudTest, capability string) (state, reason string) {
	for _, r := range view.Results {
		if r.Capability == capability {
			return r.State, r.Reason
		}
	}
	return "", ""
}

func countRequests(requests []string, want string) int {
	n := 0
	for _, r := range requests {
		if r == want {
			n++
		}
	}
	return n
}

func TestCardDAVRetriesATransientRefusalAndSyncsFromCachedURLs(t *testing.T) {
	_, dav := partitionedICloud(t)
	partitionHome := "PROPFIND " + partitionHost + " /42/carddavhome/"
	report := "REPORT " + partitionHost + " /42/carddavhome/card/"
	root := "PROPFIND " + frontHost + " /"

	dav.set(func(p *partitionedDAV) { p.refuseTimes = 2 })
	dav.drain()
	view := testICloudConnection(t)
	if state, reason := capabilityResult(view, davmirror.CapabilityCardDAV); state != "ok" || reason != "1 collection" {
		t.Fatalf("carddav after two transient refusals = %s %q, want ok with 1 collection", state, reason)
	}
	if got := countRequests(dav.drain(), partitionHome); got != 3 {
		t.Errorf("two transient refusals took %d home set requests on the partition host, want 3", got)
	}

	dav.set(func(p *partitionedDAV) { p.refuseTimes = 3 })
	view = testICloudConnection(t)
	if state, reason := capabilityResult(view, davmirror.CapabilityCardDAV); state != "failing" ||
		!strings.Contains(reason, "refused the home set request on the partition host (HTTP 401, no WWW-Authenticate challenge)") {
		t.Errorf("carddav after three transient refusals = %s %q, want failing on the partition home set", state, reason)
	}
	if got := countRequests(dav.drain(), partitionHome); got != 3 {
		t.Errorf("a lasting refusal took %d home set requests on the partition host, want 3", got)
	}
	dav.set(func(p *partitionedDAV) { p.refuseTimes = 0 })

	if got := runApp(t, "contacts", "sync"); got.code != cli.ExitOK {
		t.Fatalf("first contacts sync: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if first := dav.drain(); len(first) < 2 || first[0] != root || first[len(first)-1] != report {
		t.Errorf("the first sync after a failure did not discover from the root: %q", first)
	}
	if got := runApp(t, "contacts", "sync"); got.code != cli.ExitOK {
		t.Fatalf("second contacts sync: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if second := dav.drain(); !slices.Equal(second, []string{report}) {
		t.Errorf("the second sync sent %q, want only the address book report", second)
	}

	checked := runApp(t, "connections", "check", "--json")
	var views []icloudTest
	if err := json.Unmarshal([]byte(checked.stdout), &views); err != nil || len(views) == 0 {
		t.Fatalf("connections check: %d %q %q: %v", checked.code, checked.stdout, checked.stderr, err)
	}
	if state, reason := capabilityResult(views[0], davmirror.CapabilityCardDAV); state != "ok" || reason != "the mirror sync works" {
		t.Errorf("a health check right after a sync = %s %q, want the sync's ok result", state, reason)
	}
	if got := dav.drain(); len(got) != 0 {
		t.Errorf("a health check right after a sync sent %q, want nothing", got)
	}

	dav.set(func(p *partitionedDAV) { p.partitionRefuses = true })
	refused := runApp(t, "contacts", "sync")
	if refused.code == cli.ExitOK {
		t.Fatalf("contacts sync on a refusing partition succeeded: %q", refused.stdout)
	}
	if !strings.Contains(refused.stderr, "/<id>/") || strings.Contains(refused.stderr+refused.stdout, "/42/") {
		t.Errorf("the sync error does not mask the account id: %q", refused.stderr)
	}
	if got := dav.drain(); len(got) < 2 || got[0] != report || got[1] != root {
		t.Errorf("a refused cached report did not lead to rediscovery from the root: %q", got)
	}
	dav.set(func(p *partitionedDAV) { p.partitionRefuses = false })
	if got := runApp(t, "contacts", "sync"); got.code != cli.ExitOK {
		t.Fatalf("contacts sync after the refusal ended: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if got := dav.drain(); len(got) == 0 || got[0] != root {
		t.Errorf("the sync after a failed rediscovery reused stale URLs: %q", got)
	}
}
