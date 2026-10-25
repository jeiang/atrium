package web_test

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/auth/oidc/oidctest"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
)

var tags = regexp.MustCompile(`<[^>]*>`)

func plain(body string) string {
	return strings.Join(strings.Fields(html.UnescapeString(tags.ReplaceAllString(body, " "))), " ")
}

func squash(text string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(text, " .", ".")), " ")
}

func wizardRig(t *testing.T) (*rig, *browser) {
	t.Helper()
	r := newRig(t, rigOpts{preset: "generic"})
	r.idp.AddUser(oidctest.User{Name: "admin", Email: "boss@example.com", Groups: []string{"atrium_access", "atrium_admin"}})
	r.idp.AddUser(oidctest.User{Name: "member", Email: "friend@example.com", Groups: []string{"atrium_access"}})
	r.idp.AddUser(oidctest.User{Name: "boss", Email: "owner@example.com", Groups: []string{"team_access", "team_admin"}})
	admin := r.browser()
	admin.mustSignIn(r, "admin")
	return r, admin
}

func (r *rig) form(extra map[string]string) url.Values {
	v := url.Values{
		"preset": {"generic"}, "issuer": {r.idp.Issuer()}, "client_id": {r.idp.ClientID()},
		"access_group": {"atrium_access"}, "admin_group": {"atrium_admin"},
		"groups_claim": {"groups"}, "scopes": {"openid profile email groups"},
	}
	for k, val := range extra {
		v.Set(k, val)
	}
	return v
}

func startTest(t *testing.T, b *browser, form url.Values) reply {
	t.Helper()
	return b.post("/settings/auth/test", form)
}

func providerTab(t *testing.T, b *browser, idp *oidctest.Provider, user string, start reply) reply {
	t.Helper()
	if start.Status != http.StatusOK {
		t.Fatalf("test start: %d %.300s", start.Status, start.Body)
	}
	idp.Approve(user)
	target, ok := strings.CutPrefix(start.Header.Get("Refresh"), "0; url=")
	if !ok {
		t.Fatalf("no provider redirect: %q", start.Header.Get("Refresh"))
	}
	authz := b.get(target)
	if authz.Status != http.StatusFound {
		t.Fatalf("authorize: %d %s", authz.Status, authz.Body)
	}
	return b.get(authz.location())
}

func sessionToken(b *browser) string {
	for _, c := range b.jar.Cookies(mustURL(b.base)) {
		if c.Name == "atrium_session" {
			return c.Value
		}
	}
	return ""
}

func mustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

func activeSetting(t *testing.T, r *rig, key string) string {
	t.Helper()
	var v string
	_ = r.db.Read.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	return v
}

func TestWizardStepsMatchThePresetTable(t *testing.T) {
	r, admin := wizardRig(t)
	for _, p := range oidc.Presets() {
		page := admin.get("/settings/auth/preset?preset=" + p.Name)
		expect(t, page, http.StatusOK)
		text := squash(plain(page.Body))
		for _, step := range p.Steps {
			want := strings.NewReplacer(oidc.StepRedirect, r.url+"/auth/callback", oidc.StepAccess, "atrium_access", oidc.StepAdmin, "atrium_admin").Replace(step)
			if !strings.Contains(text, squash(want)) {
				t.Fatalf("%s: step %q missing from %q", p.Name, want, text)
			}
		}
		if p.Name == oidc.PresetPocketID {
			for _, want := range []string{
				"Open OIDC Clients and choose Add client.",
				"Set the callback URL to " + r.url + "/auth/callback.",
				"Enable PKCE.",
				"Allow the groups atrium_access and atrium_admin.",
				"Copy the client ID and secret into the form below.",
			} {
				if !strings.Contains(text, squash(want)) {
					t.Fatalf("pocket id lacks %q in %q", want, text)
				}
			}
			for _, chip := range []string{`data-action="copy-value"`, r.url + "/auth/callback"} {
				if !strings.Contains(page.Body, chip) {
					t.Fatalf("pocket id page lacks %q", chip)
				}
			}
		}
	}
	full := admin.get("/settings/auth")
	expect(t, full, http.StatusOK, "Sign-in", "Generic OIDC", "Active", "Test sign-in", `id="oidc-test"`)
	for _, p := range oidc.Presets() {
		if !strings.Contains(full.Body, ">"+p.Label()+"<") {
			t.Fatalf("provider select lacks %s", p.Label())
		}
	}
	entra := admin.get("/settings/auth/preset?preset=entra")
	expect(t, entra, http.StatusOK, "Tenant ID", `placeholder="Object ID"`)
	expect(t, admin.get("/settings/auth/preset?preset=keycloak"), http.StatusOK, "Realm URL")
	google := admin.get("/settings/auth/preset?preset=google")
	expect(t, google, http.StatusOK, "Access emails", "Admin domains")
	if strings.Contains(google.Body, "Issuer URL") || strings.Contains(google.Body, "Access group") {
		t.Fatal("google shows issuer or group fields")
	}
}

func TestWizardNeedsAnAdmin(t *testing.T) {
	r, _ := wizardRig(t)
	member := r.browser()
	member.mustSignIn(r, "member")
	expect(t, member.get("/settings/auth"), http.StatusForbidden, "Admins only")
	expect(t, member.get("/settings/auth/preset?preset=entra"), http.StatusForbidden)
	expect(t, member.post("/settings/auth/test", r.form(nil)), http.StatusForbidden)
	expect(t, member.post("/settings/auth/activate", url.Values{"fingerprint": {"x"}}), http.StatusForbidden)
	expect(t, member.get("/settings/auth/test/status"), http.StatusForbidden)
	anon := r.browser()
	expect(t, anon.get("/settings/auth"), http.StatusSeeOther)
	expect(t, anon.post("/settings/auth/test", r.form(nil)), http.StatusSeeOther)
}

func TestWizardTestSignInThenActivate(t *testing.T) {
	r, admin := wizardRig(t)
	before := sessionToken(admin)
	form := r.form(map[string]string{"access_group": "team_access", "admin_group": "team_admin"})

	idle := admin.get("/settings/auth/test/status")
	expect(t, idle, http.StatusOK, `data-state="idle"`)
	waiting := admin.get("/settings/auth/test/status?after=0")
	expect(t, waiting, http.StatusOK, "Waiting for the provider tab")

	start := startTest(t, admin, form)
	expect(t, admin.get("/settings/auth/test/status"), http.StatusOK, "Waiting for the provider tab")
	result := providerTab(t, admin, r.idp, "boss", start)
	expect(t, result, http.StatusOK, "Test passed", "team_access", "team_admin")

	if sessionToken(admin) != before {
		t.Fatal("the test replaced the session cookie")
	}
	if n := r.count("SELECT COUNT(*) FROM auth_sessions"); n != 1 {
		t.Fatalf("%d sessions after the test", n)
	}
	expect(t, admin.get("/ping"), http.StatusOK, "admin=yes")
	if got := activeSetting(t, r, oidc.KeyAccessGroup); got != "" {
		t.Fatalf("the active access group changed before activation: %q", got)
	}
	stranger := r.browser()
	r.idp.Approve("boss")
	expect(t, stranger.signIn(r, "boss"), http.StatusForbidden)

	status := admin.get("/settings/auth/test/status")
	expect(t, status, http.StatusOK, "Test passed", "Activate", "team_admin")
	fingerprint := regexp.MustCompile(`name="fingerprint" value="([0-9a-f]+)"`).FindStringSubmatch(status.Body)
	if fingerprint == nil {
		t.Fatalf("no fingerprint in %s", status.Body)
	}

	expect(t, admin.post("/settings/auth/activate", url.Values{"fingerprint": {"stale"}}), http.StatusConflict, "Test sign-in again")
	if got := activeSetting(t, r, oidc.KeyAccessGroup); got != "" {
		t.Fatal("a stale fingerprint activated the configuration")
	}
	activated := admin.post("/settings/auth/activate", url.Values{"fingerprint": {fingerprint[1]}})
	expect(t, activated, http.StatusOK, "Sign-in is active")
	if activeSetting(t, r, oidc.KeyAccessGroup) != "team_access" || activeSetting(t, r, oidc.KeyAdminGroup) != "team_admin" {
		t.Fatal("the tested groups were not saved")
	}
	if sessionToken(admin) != before {
		t.Fatal("activation replaced the session cookie")
	}
	expect(t, admin.get("/ping"), http.StatusOK, "admin=yes")
	expect(t, admin.post("/settings/auth/activate", url.Values{"fingerprint": {fingerprint[1]}}), http.StatusConflict)

	owner := r.browser()
	owner.mustSignIn(r, "boss")
	expect(t, owner.get("/settings/ping"), http.StatusOK, "admin=yes")
	old := r.browser()
	expect(t, old.signIn(r, "member"), http.StatusForbidden)
	if n := r.count("SELECT COUNT(*) FROM settings_audit WHERE key = ? AND source = 'ui'", oidc.KeyAccessGroup); n != 1 {
		t.Fatalf("%d audit rows for the activation", n)
	}
}

func TestWizardFailedTestsLeaveTheActiveConfigAlone(t *testing.T) {
	r, admin := wizardRig(t)
	snapshot := func() int {
		return r.count("SELECT COUNT(*) FROM settings") + 1000*r.count("SELECT COUNT(*) FROM settings_audit")
	}
	baseline := snapshot()

	expectFailure := func(name string, start reply, user string, idp *oidctest.Provider, status int, want string) {
		t.Helper()
		var result reply
		if start.Status == http.StatusOK && idp != nil {
			result = providerTab(t, admin, idp, user, start)
		} else {
			result = start
		}
		expect(t, result, status, "Test failed", want)
		poll := admin.get("/settings/auth/test/status")
		expect(t, poll, http.StatusOK, want)
		if strings.Contains(poll.Body, "Activate") {
			t.Fatalf("%s: a failed test offers Activate", name)
		}
		expect(t, admin.post("/settings/auth/activate", url.Values{"fingerprint": {"anything"}}), http.StatusConflict)
		if snapshot() != baseline {
			t.Fatalf("%s: the active configuration changed", name)
		}
	}

	expectFailure("member lacks admin", startTest(t, admin, r.form(nil)), "member", r.idp, http.StatusForbidden, "This account is not in `atrium_admin`.")
	expectFailure("no access", startTest(t, admin, r.form(map[string]string{"access_group": "team_access", "admin_group": "team_admin"})), "member", r.idp, http.StatusForbidden, "This account is not in `team_access`.")
	expectFailure("no groups claim", startTest(t, admin, r.form(map[string]string{"groups_claim": "roles"})), "admin", r.idp, http.StatusForbidden, "No `roles` claim in the response.")
	expectFailure("provider denies", startTest(t, admin, r.form(nil)), "", r.idp, http.StatusBadRequest, "The provider rejected the sign-in.")

	down := oidctest.New(t, oidctest.Options{})
	down.Down = true
	expectFailure("issuer down", startTest(t, admin, r.form(map[string]string{"issuer": down.Issuer()})), "", nil, http.StatusBadGateway, "The issuer did not respond.")

	nopkce := oidctest.New(t, oidctest.Options{NoS256: true})
	expectFailure("no s256", startTest(t, admin, r.form(map[string]string{"issuer": nopkce.Issuer()})), "", nil, http.StatusBadRequest, "The provider does not support PKCE S256.")

	other := oidctest.New(t, oidctest.Options{ClientSecret: "another-secret"})
	other.AddUser(oidctest.User{Name: "admin", Groups: []string{"atrium_access", "atrium_admin"}})
	expectFailure("wrong secret", startTest(t, admin, r.form(map[string]string{"issuer": other.Issuer()})), "admin", other, http.StatusBadRequest, "The provider rejected the client.")

	expectFailure("missing field", startTest(t, admin, r.form(map[string]string{"client_id": ""})), "", nil, http.StatusUnprocessableEntity, "Fields missing.")
	expectFailure("bad issuer", startTest(t, admin, r.form(map[string]string{"issuer": "ftp://nowhere"})), "", nil, http.StatusUnprocessableEntity, "Fields missing.")

	r.browser().mustSignIn(r, "admin")
	expect(t, admin.get("/ping"), http.StatusOK, "admin=yes")
}

func TestWizardRefusesAReplayedOrForeignCallback(t *testing.T) {
	r, admin := wizardRig(t)
	start := startTest(t, admin, r.form(nil))
	r.idp.Approve("admin")
	target := strings.TrimPrefix(start.Header.Get("Refresh"), "0; url=")
	authz := admin.get(target)
	callback := authz.location()

	foreign := r.browser()
	expect(t, foreign.get(callback), http.StatusBadRequest, "Test failed")
	if st := admin.get("/settings/auth/test/status"); strings.Contains(st.Body, "Activate") {
		t.Fatal("a foreign browser completed the test")
	}

	start = startTest(t, admin, r.form(nil))
	result := providerTab(t, admin, r.idp, "admin", start)
	expect(t, result, http.StatusOK, "Test passed")
	expect(t, admin.get(callback), http.StatusBadRequest)
}

func TestWizardPinnedFieldsAreReadOnlyAndWin(t *testing.T) {
	r, admin := wizardRig(t)
	settings.Default.SetPins(secretPin{secrets.OIDCClientSecret: "s3cret", oidc.KeyClientID: r.idp.ClientID(), oidc.KeyAccessGroup: "atrium_access"})
	t.Cleanup(func() { settings.Default.SetPins(nil) })
	page := admin.get("/settings/auth")
	expect(t, page, http.StatusOK)
	for _, id := range []string{"oidc-client-id", "oidc-access-group", "oidc-client-secret"} {
		field := regexp.MustCompile(`<input id="` + id + `"[^>]*>`).FindString(page.Body)
		if !strings.Contains(field, " disabled") {
			t.Fatalf("%s is editable: %q", id, field)
		}
	}
	if n := strings.Count(page.Body, ">Pinned<"); n < 3 {
		t.Fatalf("%d pinned badges", n)
	}
	adminField := regexp.MustCompile(`<input id="oidc-admin-group"[^>]*>`).FindString(page.Body)
	if strings.Contains(adminField, " disabled") {
		t.Fatal("an unpinned field is read-only")
	}
	result := providerTab(t, admin, r.idp, "admin", startTest(t, admin, r.form(map[string]string{"client_id": "forged", "access_group": "forged"})))
	expect(t, result, http.StatusOK, "Test passed", "atrium_access")
}

func TestWizardGoogleFlowUsesEmailAllowlists(t *testing.T) {
	r := newRig(t, rigOpts{preset: "google", settings: map[string]string{"oidc_access_emails": "friend@example.com", "oidc_admin_emails": "boss@example.com"}})
	settings.Default.SetPins(secretPin{secrets.OIDCClientSecret: "s3cret", oidc.KeyIssuer: r.idp.Issuer()})
	t.Cleanup(func() { settings.Default.SetPins(nil) })
	r.idp.AddUser(oidctest.User{Name: "admin", Email: "boss@example.com", EmailVerified: true})
	r.idp.AddUser(oidctest.User{Name: "friend", Email: "friend@example.com", EmailVerified: true})
	admin := r.browser()
	admin.mustSignIn(r, "admin")
	form := url.Values{
		"preset": {"google"}, "client_id": {r.idp.ClientID()}, "scopes": {"openid email profile"},
		"access_emails": {"friend@example.com, boss@example.com"}, "admin_emails": {"boss@example.com"},
	}
	denied := providerTab(t, admin, r.idp, "friend", startTest(t, admin, form))
	expect(t, denied, http.StatusForbidden, "This account is not an administrator.")
	passed := providerTab(t, admin, r.idp, "admin", startTest(t, admin, form))
	expect(t, passed, http.StatusOK, "Test passed")
	status := admin.get("/settings/auth/test/status")
	fingerprint := regexp.MustCompile(`name="fingerprint" value="([0-9a-f]+)"`).FindStringSubmatch(status.Body)
	if fingerprint == nil {
		t.Fatalf("no fingerprint in %s", status.Body)
	}
	expect(t, admin.post("/settings/auth/activate", url.Values{"fingerprint": {fingerprint[1]}}), http.StatusOK, "Sign-in is active")
	if got := activeSetting(t, r, oidc.KeyAccessEmails); got != "friend@example.com, boss@example.com" {
		t.Fatalf("access emails %q", got)
	}
	r.browser().mustSignIn(r, "friend")
}
