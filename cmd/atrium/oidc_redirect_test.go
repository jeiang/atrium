package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestWimOIDCSignInAndProbeDoNotFollowAProviderRedirectToAnotherHost(t *testing.T) {
	t.Parallel()
	s := wimOIDCSite(t)
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		http.Error(w, "elsewhere", http.StatusTeapot)
	}))
	t.Cleanup(other.Close)

	b := s.browser()
	s.idp.Approve("admin")
	start := b.post("/login", url.Values{"next": {""}})
	if start.status != http.StatusSeeOther {
		t.Fatalf("POST /login: %d %.300s", start.status, start.body)
	}
	authz := b.get(start.location())
	if authz.status != http.StatusFound {
		t.Fatalf("authorize: %d %s", authz.status, authz.body)
	}
	s.idp.MoveTo(other.URL)
	b.get(authz.location())
	if after := b.get("/settings/auth"); after.status != http.StatusSeeOther {
		t.Fatalf("a sign-in whose token request was redirected left a session: %d", after.status)
	}
	if n := elsewhere.Load(); n != 0 {
		t.Fatalf("the sign-in followed the provider redirect: %d requests to the other host", n)
	}

	tested := runApp(t, "connections", "test", "service/oidc", "--json")
	report := connectionReportFor(t, connectionReports(t, tested.stdout), "service/oidc")
	if got := connectionResult(t, report, "discovery"); got == "ok" {
		t.Fatalf("discovery = ok behind a redirect to another host")
	}
	if n := elsewhere.Load(); n != 0 {
		t.Fatalf("the probe followed the provider redirect: %d requests to the other host", n)
	}
}
