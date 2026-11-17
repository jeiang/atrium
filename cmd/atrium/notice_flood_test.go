package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/apps/mail/mailtest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/passrun"
)

func TestAFlappingScheduledSyncSendsOneNoticePerCauseAndABoundedNumberWhileItFlaps(t *testing.T) {
	requireCipher(t)
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	dir, srv := davFixture(t)
	upstream, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var refuse atomic.Bool
	forward := httputil.NewSingleHostReverseProxy(upstream)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuse.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		forward.ServeHTTP(w, r)
	}))
	t.Cleanup(provider.Close)
	host := accountsFile(t, dir, mailAccount{"icloud", "icloud", "operator@icloud.invalid", davPassword, false})
	f, err := os.OpenFile(host, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(f, `
[connections."icloud/icloud"]
kind = "icloud"
label = "iCloud DAV"
config = { apple_id = "operator@icloud.invalid" }
capabilities = ["caldav", "carddav"]
credential_files = { "mail/icloud/password" = %q }
`, filepath.Join(dir, "icloud.password")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", host)
	setEnv(t, davmirror.CardDAVEnv, provider.URL+"/")
	t.Setenv(davmirror.CalDAVEnv, srv.URL+"/")
	t.Setenv(davmirror.CardDAVEnv, provider.URL+"/")
	fake := mailtest.NewFakeClient()
	fake.AddBox("INBOX", mailtest.NewBox(1))
	strictIMAPRig(t, map[string]string{"operator@icloud.invalid": davPassword}, map[string]*mailtest.FakeClient{"icloud": fake})
	strictSMTPRig(t, map[string]string{"operator@icloud.invalid": davPassword})
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)
	base, _ := startServeLogged(t, dir)
	token := localAPIToken(t, "api")
	s := &apiSite{t: t, base: base}
	id := "icloud/icloud"
	if got := runApp(t, "connections", "test", id); got.code != cli.ExitOK {
		t.Fatalf("connections test %s: exit %d stdout %q stderr %q", id, got.code, got.stdout, got.stderr)
	}
	label, _, _ := runtimeRow(t, dir, id)
	connectionBefore := connectionNotices(t, dir, label)
	notices := func() (connection, failed, stale int) {
		return connectionNotices(t, dir, label) - connectionBefore,
			titledNotices(t, d, "Failed: "+passrun.CardDAV),
			titledNotices(t, d, "atrium: "+passrun.CardDAV+" is stale")
	}
	apiState := func() string {
		got := s.do(http.MethodGet, "/api/v1/connections", token, nil)
		if got.status != http.StatusOK {
			t.Fatalf("GET /api/v1/connections: %d %s", got.status, got.body)
		}
		var page struct {
			Data struct {
				Connections []struct {
					ID    string `json:"id"`
					State string `json:"state"`
				} `json:"connections"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(got.body), &page); err != nil {
			t.Fatalf("connections body: %v %s", err, got.body)
		}
		for _, c := range page.Data.Connections {
			if c.ID == id {
				return c.State
			}
		}
		t.Fatalf("connections body lacks %s: %s", id, got.body)
		return ""
	}
	run := func(refused bool, want string) {
		t.Helper()
		refuse.Store(refused)
		before := schedulerRuns(t, d, passrun.CardDAV)
		asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", float64(time.Now().Unix())-1, davJobKeys[1])
		waitUntil(t, 60*time.Second, "the scheduled CardDAV run", func() bool {
			return schedulerRuns(t, d, passrun.CardDAV) > before && schedulerTerminal(d, passrun.CardDAV)()
		})
		if status, text := schedulerStatus(t, d, passrun.CardDAV); (status == "error") != refused {
			t.Fatalf("CardDAV run status %s (%s), refused %v", status, text, refused)
		}
		waitUntil(t, 10*time.Second, "the connection to reach "+want, func() bool { return connectionState(t, dir, id) == want })
		time.Sleep(time.Second)
	}
	expect := func(when string, connection int) {
		t.Helper()
		c, failed, stale := notices()
		if c != connection || failed != 0 || stale != 0 {
			t.Fatalf("%s: %d connection, %d job-failed and %d stale notices, want %d, 0 and 0", when, c, failed, stale, connection)
		}
	}

	run(true, "needs_reauth")
	expect("one refused run", 1)
	run(true, "needs_reauth")
	expect("a lasting refusal", 1)
	run(false, "ok")
	expect("the first recovery", 2)
	if got := apiState(); got != "ok" {
		t.Fatalf("API state after the recovery = %q, want ok", got)
	}
	for i := range 4 {
		refused := i%2 == 0
		want := "ok"
		if refused {
			want = "needs_reauth"
		}
		run(refused, want)
		expect(fmt.Sprintf("flip %d", i+1), 2)
	}
	if got := apiState(); got != "ok" {
		t.Fatalf("API state after the last recovery = %q, want ok", got)
	}
}
