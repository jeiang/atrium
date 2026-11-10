package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeclaredSettingGroupSavesFromItsModulePage(t *testing.T) {
	t.Parallel()
	base := startSettingsServe(t, nil)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	status, body, _ := fetch(t, client, http.MethodGet, base+"/settings/assistant", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /settings/assistant = %d", status)
	}
	for _, want := range []string{`data-group="assistant.spending"`, `name="thread_cost_cap_usd"`, "Thread cost cap (USD)"} {
		if !strings.Contains(body, want) {
			t.Errorf("/settings/assistant does not render %s", want)
		}
	}
	const action = "/settings/groups/assistant/spending"
	status, body, _ = fetch(t, client, http.MethodPost, base+action, url.Values{"thread_cost_cap_usd": {"12"}})
	if status != http.StatusOK || !strings.Contains(body, "data-saved") {
		t.Fatalf("POST %s = %d, saved marker %v", action, status, strings.Contains(body, "data-saved"))
	}
	if row := runSettings(t, "get", "thread_cost_cap_usd").row(t); row.Value == nil || *row.Value != "12" || row.Source == nil || *row.Source != "db" {
		t.Errorf("thread_cost_cap_usd = %+v, want 12 from the database", row)
	}
	status, body, _ = fetch(t, client, http.MethodPost, base+action, url.Values{"thread_cost_cap_usd": {"-1"}})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "must be greater than 0") {
		t.Errorf("POST an invalid value = %d: %.300s", status, body)
	}
	if row := runSettings(t, "get", "thread_cost_cap_usd").row(t); row.Value == nil || *row.Value != "12" {
		t.Errorf("a refused save changed thread_cost_cap_usd to %+v", row)
	}
	if status, _, _ := fetch(t, client, http.MethodPost, base+"/settings/groups/assistant/nope", url.Values{}); status != http.StatusNotFound {
		t.Errorf("POST an unknown group = %d, want 404", status)
	}
}

func TestExternalModuleSettingsGroupGetsAPage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	base, _, _ := startServeApp(t, newTestApp(t, extModules()), &lockedBuffer{})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	status, body, _ := fetch(t, client, http.MethodGet, base+"/settings/"+extName, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /settings/%s = %d", extName, status)
	}
	for _, want := range []string{`<a href="/settings/` + extName + `" aria-current="page">Extdemo</a>`, `data-group="` + extName + `.general"`, "Greeting", `value="hello"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the module page does not render %s", want)
		}
	}
	status, body, _ = fetch(t, client, http.MethodPost, base+"/settings/groups/"+extName+"/general", url.Values{extGreeting: {"good day"}})
	if status != http.StatusOK || !strings.Contains(body, `value="good day"`) {
		t.Errorf("POST the group = %d, saved value shown %v", status, strings.Contains(body, `value="good day"`))
	}
	if !strings.Contains(body, `data-module="`+extName+`" data-enabled="false"`) {
		t.Errorf("the module page does not show the module as off")
	}
	status, body, _ = fetch(t, client, http.MethodPost, base+"/settings/modules/"+extName, url.Values{"enabled": {"true"}})
	if status != http.StatusOK || !strings.Contains(body, `data-module="`+extName+`" data-enabled="true"`) || !strings.Contains(body, "data-restart") {
		t.Errorf("turning the module on = %d, on %v, restart note %v", status, strings.Contains(body, `data-enabled="true"`), strings.Contains(body, "data-restart"))
	}
}
