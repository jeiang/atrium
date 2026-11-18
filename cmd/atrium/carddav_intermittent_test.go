package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

type backoffHold struct {
	Refusals int       `json:"refusals"`
	Until    time.Time `json:"backoff_until"`
}

func storedCardDAV(t *testing.T, dir string) (state, reason string, hold backoffHold) {
	t.Helper()
	var raw string
	if err := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t)).Read.QueryRow("SELECT results_json FROM connections WHERE id = 'icloud/icloud'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var results []struct {
		Capability string          `json:"capability"`
		State      string          `json:"state"`
		Reason     string          `json:"reason"`
		Data       json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal([]byte(raw), &results)
	for _, r := range results {
		if r.Capability == davmirror.CapabilityCardDAV {
			_ = json.Unmarshal(r.Data, &hold)
			return r.State, r.Reason, hold
		}
	}
	return "", "", backoffHold{}
}

func endBackoff(t *testing.T, dir string) {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	if _, err := d.Write.Exec(`UPDATE connections SET results_json = (SELECT json_group_array(CASE
		WHEN json_type(value, '$.data.backoff_until') IS NOT NULL THEN json_set(value, '$.data.backoff_until', ?) ELSE json(value) END) FROM json_each(results_json))
		WHERE id = 'icloud/icloud'`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

func near(got, want time.Time) bool {
	return got.After(want.Add(-2*time.Minute)) && got.Before(want.Add(2*time.Minute))
}

func TestCardDAVRetriesATransientRefusalAndSyncsFromCachedURLs(t *testing.T) {
	dir, dav := partitionedICloud(t)
	partitionHome := "PROPFIND " + partitionHost + " /42/carddavhome/"
	report := "REPORT " + partitionHost + " /42/carddavhome/card/"
	root := "PROPFIND " + frontHost + " /"

	dav.set(func(p *partitionedDAV) { p.refuseTimes = 1 })
	dav.drain()
	view := testICloudConnection(t)
	if state, reason := capabilityResult(view, davmirror.CapabilityCardDAV); state != "ok" || reason != "1 collection" {
		t.Fatalf("carddav after one transient refusal = %s %q, want ok with 1 collection", state, reason)
	}
	if got := countRequests(dav.drain(), partitionHome); got != 2 {
		t.Errorf("one transient refusal took %d home set requests on the partition host, want 2", got)
	}

	dav.set(func(p *partitionedDAV) { p.refuseTimes = 2 })
	view = testICloudConnection(t)
	if state, reason := capabilityResult(view, davmirror.CapabilityCardDAV); state != "failing" ||
		!strings.Contains(reason, "refused the home set request on the partition host (HTTP 401, no WWW-Authenticate challenge)") ||
		!strings.Contains(reason, "atrium sends iCloud no request for this until ") {
		t.Errorf("carddav after two transient refusals = %s %q, want failing on the partition home set with the backoff end", state, reason)
	}
	if got := countRequests(dav.drain(), partitionHome); got != 2 {
		t.Errorf("a lasting refusal took %d home set requests on the partition host, want 2", got)
	}
	if _, _, hold := storedCardDAV(t, dir); hold.Refusals != 1 || !near(hold.Until, time.Now().Add(time.Hour)) {
		t.Errorf("the first refusal stored %+v, want 1 refusal and an hour of backoff", hold)
	}

	dav.set(func(p *partitionedDAV) { p.refuseTimes = 1 })
	view = testICloudConnection(t)
	if state, reason := capabilityResult(view, davmirror.CapabilityCardDAV); state != "failing" || !strings.HasPrefix(reason, "tested at the operator's request during a backoff that was to end ") {
		t.Errorf("an explicit test during the backoff = %s %q, want failing and saying it ran at the operator's request", state, reason)
	}
	if got := countRequests(dav.drain(), partitionHome); got != 1 {
		t.Errorf("a refusal right after a refused run took %d home set requests on the partition host, want 1 with no retry", got)
	}
	if _, _, hold := storedCardDAV(t, dir); hold.Refusals != 2 || !near(hold.Until, time.Now().Add(3*time.Hour)) {
		t.Errorf("the second refusal stored %+v, want 2 refusals and 3 hours of backoff", hold)
	}

	view = testICloudConnection(t)
	if state, reason := capabilityResult(view, davmirror.CapabilityCardDAV); state != "ok" || !strings.HasSuffix(reason, ": 1 collection") {
		t.Errorf("an explicit test that works during the backoff = %s %q, want ok", state, reason)
	}
	if _, _, hold := storedCardDAV(t, dir); hold.Refusals != 0 {
		t.Errorf("a working test kept the backoff %+v", hold)
	}
	dav.drain()

	if got := runApp(t, "contacts", "sync"); got.code != cli.ExitOK {
		t.Fatalf("first contacts sync: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if first := dav.drain(); !slices.Equal(first, []string{report}) {
		t.Errorf("the first sync after a working test did not reuse its URLs: %q", first)
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

func lastCardDAVRun(t *testing.T, dir string) (status, text string) {
	t.Helper()
	if err := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t)).Read.QueryRow(
		"SELECT r.status, COALESCE(r.error, '') FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.declaration_key = ? ORDER BY r.id DESC LIMIT 1", davJobKeys[1]).Scan(&status, &text); err != nil {
		t.Fatal(err)
	}
	return status, text
}

func TestDAVRefusalBacksOffAcrossARestartAndASuccessResetsIt(t *testing.T) {
	dir, dav := partitionedICloud(t)
	partitionHome := "PROPFIND " + partitionHost + " /42/carddavhome/"
	base, _, stop := startServeStoppable(t, dir)
	token := localAPIToken(t, "api")
	site := &apiSite{t: t, base: base}
	sync := func() apiReply { return site.do(http.MethodPost, "/api/v1/contacts/sync", token, nil) }
	if state, _ := capabilityResult(testICloudConnection(t), davmirror.CapabilityCardDAV); state != "ok" {
		t.Fatalf("carddav before the refusal = %s", state)
	}

	dav.set(func(p *partitionedDAV) { p.partitionRefuses = true })
	dav.drain()
	if got := sync(); got.status == http.StatusOK {
		t.Fatalf("a refused contacts sync succeeded: %s", got.body)
	}
	if got := countRequests(dav.drain(), partitionHome); got != 2 {
		t.Errorf("the refused sync sent %d partition home set requests, want 2 (one retry)", got)
	}
	state, reason, hold := storedCardDAV(t, dir)
	if state != "failing" || hold.Refusals != 1 || !near(hold.Until, time.Now().Add(time.Hour)) || !strings.Contains(reason, "atrium sends iCloud no request for this until "+hold.Until.UTC().Format("2006-01-02 15:04 UTC")) {
		t.Fatalf("after the first refusal: %s %q %+v, want failing for an hour with the end in the reason", state, reason, hold)
	}

	if got := sync(); got.status == http.StatusOK || !strings.Contains(got.body, "skipped, backing off until ") {
		t.Errorf("a sync during the backoff = %d %s, want the skip", got.status, got.body)
	}
	if status, text := lastCardDAVRun(t, dir); status != "skipped_dependency" || !strings.Contains(text, "skipped, backing off until ") {
		t.Errorf("the skipped sync recorded %s %q, want skipped_dependency with the backoff end", status, text)
	}
	runApp(t, "connections", "check")
	if got := dav.drain(); len(got) != 0 {
		t.Errorf("a sync and a health check during the backoff sent %q", got)
	}
	if after, afterReason, _ := storedCardDAV(t, dir); after != "failing" || afterReason != reason {
		t.Errorf("a health check during the backoff stored %s %q, want the stored %q", after, afterReason, reason)
	}

	stop()
	base, _, _ = startServeStoppable(t, dir)
	site = &apiSite{t: t, base: base}
	sync()
	runApp(t, "connections", "check")
	if got := dav.drain(); len(got) != 0 {
		t.Errorf("a restart during the backoff sent %q", got)
	}

	endBackoff(t, dir)
	if got := sync(); got.status == http.StatusOK {
		t.Fatalf("the sync after the backoff succeeded against a refusing partition: %s", got.body)
	}
	if got := countRequests(dav.drain(), partitionHome); got != 1 {
		t.Errorf("the attempt after a backoff sent %d partition home set requests, want 1 with no retry", got)
	}
	if _, _, hold := storedCardDAV(t, dir); hold.Refusals != 2 || !near(hold.Until, time.Now().Add(3*time.Hour)) {
		t.Errorf("the second refusal stored %+v, want 3 hours", hold)
	}

	endBackoff(t, dir)
	dav.set(func(p *partitionedDAV) { p.partitionRefuses = false })
	if got := sync(); got.status != http.StatusOK {
		t.Fatalf("the sync after the backoff with a working partition: %d %s", got.status, got.body)
	}
	if state, _, hold := storedCardDAV(t, dir); state != "ok" || hold.Refusals != 0 {
		t.Errorf("a working sync left %s %+v, want ok with no backoff", state, hold)
	}

	dav.set(func(p *partitionedDAV) { p.partitionRefuses, p.retryAfter = true, "18000" })
	sync()
	if _, _, hold := storedCardDAV(t, dir); hold.Refusals != 1 || !near(hold.Until, time.Now().Add(5*time.Hour)) {
		t.Errorf("a refusal after a success with Retry-After 5 hours stored %+v, want 1 refusal and 5 hours", hold)
	}
}

func TestADAVRefusalHoldSurvivesTheHostDroppingAndDeclaringTheConnectionAgain(t *testing.T) {
	dir, dav := partitionedICloud(t)
	base, logs, _ := startServeWith(t, dir, true)
	token := localAPIToken(t, "api")
	site := &apiSite{t: t, base: base}
	sync := func() apiReply { return site.do(http.MethodPost, "/api/v1/contacts/sync", token, nil) }
	if state, _ := capabilityResult(testICloudConnection(t), davmirror.CapabilityCardDAV); state != "ok" {
		t.Fatalf("carddav before the refusal = %s", state)
	}
	dav.set(func(p *partitionedDAV) { p.partitionRefuses = true })
	sync()
	_, reason, hold := storedCardDAV(t, dir)
	if hold.Refusals != 1 {
		t.Fatalf("after the refusal %q %+v, want one refusal held", reason, hold)
	}
	for _, want := range []string{"dav request failed", "dav requests paused after a refusal"} {
		line := ""
		for l := range strings.SplitSeq(logs.String(), "\n") {
			if strings.Contains(l, want) {
				line = l
			}
		}
		if !strings.Contains(line, "INTEGRATION=icloud/icloud") || !strings.Contains(line, "CAPABILITY=carddav") {
			t.Errorf("the %q log line %q does not name the connection and the capability", want, line)
		}
	}

	host := filepath.Join(dir, "host.toml")
	declared, err := os.ReadFile(host)
	if err != nil {
		t.Fatal(err)
	}
	cut := strings.Index(string(declared), "\n[connections.\"icloud/icloud\"]")
	if cut < 0 {
		t.Fatalf("the host file declares no icloud connection:\n%s", declared)
	}
	if err := os.WriteFile(host, declared[:cut], 0o600); err != nil {
		t.Fatal(err)
	}
	runApp(t, "connections", "list")
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	var state, rowReason string
	if err := d.Read.QueryRow("SELECT state, reason FROM connections WHERE id = 'icloud/icloud'").Scan(&state, &rowReason); err != nil {
		t.Fatal(err)
	}
	if state != "disabled" || rowReason != "removed from host configuration" {
		t.Fatalf("the dropped connection is %s %q, want disabled as removed from the host", state, rowReason)
	}
	if err := os.WriteFile(host, declared, 0o600); err != nil {
		t.Fatal(err)
	}
	dav.drain()
	runApp(t, "connections", "check")
	sync()
	if got := dav.drain(); len(got) != 0 {
		t.Errorf("declaring the held connection again sent %q", got)
	}
	if after, afterReason, afterHold := storedCardDAV(t, dir); after != "failing" || afterHold != hold || afterReason != reason {
		t.Errorf("declaring the held connection again stored %s %q %+v, want the held %q %+v", after, afterReason, afterHold, reason, hold)
	}
}

func TestAChallengeLessCardDAVRefusalFailsWithoutBlamingThePasswordAndLeavesCalDAVRunning(t *testing.T) {
	requireCipher(t)
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	dir, srv := davFixture(t)
	sharedICloudHost(t, dir, davPassword)
	t.Setenv(davmirror.CalDAVEnv, srv.URL+"/")
	t.Setenv(davmirror.CardDAVEnv, srv.URL+"/")
	srv.RefuseCards.Store(true)
	view := testICloudConnection(t)
	card, cardReason := capabilityResult(view, davmirror.CapabilityCardDAV)
	if card != "failing" || !strings.Contains(cardReason, "iCloud refused the root request on the configured host (HTTP 401, no WWW-Authenticate challenge) without asking for a password again") ||
		strings.Contains(cardReason, "app password") {
		t.Errorf("a challenge-less carddav root refusal = %s %q, want failing without a word on the app password", card, cardReason)
	}
	if cal, calReason := capabilityResult(view, davmirror.CapabilityCalDAV); cal != "ok" {
		t.Errorf("caldav beside the carddav refusal = %s %q, want ok", cal, calReason)
	}
	if view.State != "failing" {
		t.Errorf("the connection beside one refused capability is %s %q, want failing, not needs_reauth", view.State, view.Reason)
	}
	if _, _, hold := storedCardDAV(t, dir); hold.Refusals != 1 {
		t.Fatalf("the carddav refusal stored %+v, want one refusal held", hold)
	}

	cards := srv.CardRequests.Load()
	if got := runApp(t, "contacts", "sync"); got.code == cli.ExitOK || !strings.Contains(got.stderr, "skipped, backing off until ") {
		t.Errorf("contacts sync during the carddav hold = %d %q %q, want the skip", got.code, got.stdout, got.stderr)
	}
	if got := runApp(t, "calendar", "sync"); got.code != cli.ExitOK {
		t.Errorf("calendar sync during the carddav hold = %d %q %q, want it to run", got.code, got.stdout, got.stderr)
	}
	runApp(t, "connections", "check")
	if got := srv.CardRequests.Load() - cards; got != 0 {
		t.Errorf("syncs and a check during the carddav hold sent %d carddav requests", got)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	var calState string
	if err := d.Read.QueryRow(`SELECT json_extract(value, '$.state') FROM connections, json_each(results_json)
		WHERE connections.id = 'icloud/icloud' AND json_extract(value, '$.capability') = 'caldav'`).Scan(&calState); err != nil {
		t.Fatal(err)
	}
	if after, _, hold := storedCardDAV(t, dir); calState != "ok" || after != "failing" || hold.Refusals != 1 {
		t.Errorf("after the syncs and the check caldav is %s and carddav %s %+v, want ok beside the held failing carddav", calState, after, hold)
	}
}

func TestAPassingConnectionTestEndsADAVHoldWhileAPassingTraceSavesNothing(t *testing.T) {
	dir, dav := partitionedICloud(t)
	dav.set(func(p *partitionedDAV) { p.partitionRefuses = true })
	testICloudConnection(t)
	state, reason, hold := storedCardDAV(t, dir)
	if state != "failing" || hold.Refusals != 1 {
		t.Fatalf("the refused test stored %s %q %+v, want one refusal held", state, reason, hold)
	}
	dav.set(func(p *partitionedDAV) { p.partitionRefuses = false })

	if got := runApp(t, "connections", "test", "icloud/icloud", "--trace", "carddav"); got.code != cli.ExitOK {
		t.Fatalf("traced test after the refusal cleared = %d %q %q, want a pass", got.code, got.stdout, got.stderr)
	}
	if after, afterReason, afterHold := storedCardDAV(t, dir); after != state || afterReason != reason || afterHold != hold {
		t.Errorf("a passing trace stored %s %q %+v, want the held %s %q %+v kept", after, afterReason, afterHold, state, reason, hold)
	}

	view := testICloudConnection(t)
	if card, cardReason := capabilityResult(view, davmirror.CapabilityCardDAV); card != "ok" {
		t.Errorf("connections test after the refusal cleared = %s %q, want ok", card, cardReason)
	}
	if after, afterReason, afterHold := storedCardDAV(t, dir); after != "ok" || afterHold.Refusals != 0 {
		t.Errorf("a passing connections test stored %s %q %+v, want ok with no hold", after, afterReason, afterHold)
	}
}
