package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"

	"github.com/jeiang/atrium/internal/module"
)

func TestModulesSettingsPageIsGone(t *testing.T) {
	t.Parallel()
	base := startServe(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if code, _, _ := fetch(t, client, http.MethodGet, base+"/settings/modules", nil); code != http.StatusNotFound {
		t.Errorf("GET /settings/modules = %d, want 404", code)
	}
	if code, _, _ := fetch(t, client, http.MethodPost, base+"/settings/modules/"+module.Contacts, url.Values{"on": {"true"}}); code != http.StatusNotFound {
		t.Errorf("POST /settings/modules/%s = %d, want 404", module.Contacts, code)
	}
}
