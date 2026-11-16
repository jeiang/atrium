package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	connectionsapp "github.com/jeiang/atrium/internal/connections/application"
	"github.com/jeiang/atrium/internal/core/coremodule"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/weather"
	"github.com/jeiang/atrium/internal/web"
)

func strictJSON[T any](t *testing.T, path string, got apiReply) T {
	t.Helper()
	if got.status != http.StatusOK {
		t.Fatalf("%s: status %d: %.600s", path, got.status, got.body)
	}
	dec := json.NewDecoder(strings.NewReader(got.body))
	dec.DisallowUnknownFields()
	var v T
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("%s does not match its declared type: %v: %.600s", path, err, got.body)
	}
	return v
}

func schemaField(t *testing.T, schema coremodule.SettingsSchema, key string) coremodule.SettingsSchemaField {
	t.Helper()
	for _, p := range schema.Pages {
		for _, f := range p.Fields {
			if f.Key == key {
				return f
			}
		}
	}
	t.Fatalf("settings schema has no %s", key)
	return coremodule.SettingsSchemaField{}
}

func connectionField(t *testing.T, schema connectionsapp.ConnectionsSchema, id, name string) connectionsapp.ConnectionField {
	t.Helper()
	for _, c := range schema.Connections {
		if string(c.ID) != id {
			continue
		}
		for _, f := range c.Fields {
			if f.Name == name {
				return f
			}
		}
	}
	t.Fatalf("connections schema has no %s field %s", id, name)
	return connectionsapp.ConnectionField{}
}

func TestReadModelsOnBothSurfaces(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	fakeOpenMeteo(t, dir)
	seedSettings(t, map[string]string{weather.KeyLatitude: "1.25", weather.KeyLongitude: "2.5", weather.KeyLocationName: "Lowmarsh"})
	host, _ := f.lookup("ATRIUM_HOST_CONFIG")
	setEnv(t, "ATRIUM_HOST_CONFIG", hostConfigFrom(t, host, dir, "host-pinned.toml", nil, "\n[core]\n"+module.FlagKey(module.Mail)+" = true\n"))
	s := startAPISite(t, dir, api)
	token := s.token("owner")
	browser := newWebClient(t, s.base, nil)
	browser.http = oidcBrowser(t, s.base, s.idp, "owner")
	browser.session("oidc")

	const secret = "read-model-secret-never-returned"
	if got := apiData(t, s.do(http.MethodPut, "/api/v1/settings/secrets/"+secrets.ActionLinkKey, token, map[string]any{"value": secret}), http.StatusOK); got["status"] != "set" {
		t.Fatalf("secret set: %v", got)
	}
	project := s.do(http.MethodPost, "/api/v1/projects", token, map[string]any{"name": "Library", "instructions": "", "effort": "high", "context_limit": 200000})
	if project.status != http.StatusCreated {
		t.Fatalf("create project: %d %s", project.status, project.body)
	}
	for _, name := range []string{"one.txt", "two.txt", "three.txt"} {
		body, media := uploadBody(t, nil, name, "notes for "+name)
		if up := s.do(http.MethodPost, projectPath(number(t, project.json["id"]), "/files"), token, body, "Content-Type", media); up.status != http.StatusCreated {
			t.Fatalf("upload %s: %d %s", name, up.status, up.body)
		}
	}

	var bodies []string
	both := func(path string) apiReply {
		t.Helper()
		bearer := s.do(http.MethodGet, "/api/v1"+path, token, nil)
		cookie := browser.do(http.MethodGet, "/web/v1"+path, nil)
		if bearer.status != http.StatusOK || cookie.status != http.StatusOK || bearer.body != cookie.body {
			t.Fatalf("%s differs between the surfaces: %d %.300s / %d %.300s", path, bearer.status, bearer.body, cookie.status, cookie.body)
		}
		if tag := bearer.header.Get("ETag"); tag == "" || s.do(http.MethodGet, "/api/v1"+path, token, nil, "If-None-Match", tag).status != http.StatusNotModified {
			t.Fatalf("%s is not conditional: ETag %q", path, tag)
		}
		bodies = append(bodies, bearer.body)
		return bearer
	}

	shell := strictJSON[coremodule.Shell](t, "/shell", both("/shell"))
	review := false
	for _, sec := range shell.Sections {
		review = review || sec.Key == "review" && sec.Badge && len(sec.Tabs) > 0
	}
	mailOn := false
	for _, m := range shell.Modules {
		mailOn = mailOn || m.Name == "mail" && m.State == "enabled"
	}
	if shell.Version == "" || !review || !mailOn || shell.Mail == nil || shell.Mail.Mode == "" || shell.SettingsURL == "" {
		t.Fatalf("shell: %+v", shell)
	}

	about := strictJSON[coremodule.AppAbout](t, "/about", both("/about"))
	if about.SchemaVersion != db.LatestSchemaVersion() || about.APIVersion != 1 || about.Version != shell.Version || len(about.Modules) != len(shell.Modules) {
		t.Fatalf("about: %+v", about)
	}

	settingsSchema := strictJSON[coremodule.SettingsSchema](t, "/settings/schema", both("/settings/schema"))
	pinned := schemaField(t, settingsSchema, module.FlagKey(module.Mail))
	if pinned.Editable || pinned.Source == nil || *pinned.Source != "pin" || pinned.Value == nil || *pinned.Value != "true" {
		t.Fatalf("host-pinned setting: %+v", pinned)
	}
	editable := 0
	for _, p := range settingsSchema.Pages {
		for _, field := range p.Fields {
			if field.Editable && field.Source != nil && *field.Source != "pin" {
				editable++
			}
		}
	}
	if editable == 0 {
		t.Fatalf("settings schema offers no editable setting: %+v", settingsSchema)
	}
	for _, p := range settingsSchema.Pages {
		for _, field := range p.Fields {
			if field.Secret && (field.Value != nil || field.Default != nil || field.Editable) {
				t.Fatalf("secret setting %s carries a value: %+v", field.Key, field)
			}
		}
	}

	conns := strictJSON[connectionsapp.ConnectionsSchema](t, "/connections/schema", both("/connections/schema"))
	if key := connectionField(t, conns, "service/anthropic", "api_key"); !key.Secret || !key.HostOwned || !key.Present || key.Type != connectionsapp.FieldSecret {
		t.Fatalf("host credential field: %+v", key)
	}
	if u := connectionField(t, conns, "service/weather", "url"); u.Type != connectionsapp.FieldURL || !u.HostOwned {
		t.Fatalf("host url field: %+v", u)
	}
	if len(conns.Kinds) == 0 {
		t.Fatal("connections schema lists no kinds")
	}

	first := strictJSON[web.List[asstapp.LibraryEntry]](t, "/files", both("/files?limit=2"))
	if len(first.Items) != 2 || first.NextCursor == nil || first.Items[0].Name != "three.txt" || first.Items[0].ProjectName != "Library" {
		t.Fatalf("first library page: %+v", first)
	}
	second := strictJSON[web.List[asstapp.LibraryEntry]](t, "/files", both("/files?limit=2&cursor="+url.QueryEscape(*first.NextCursor)))
	if len(second.Items) != 1 || second.NextCursor != nil || second.Items[0].Name != "one.txt" {
		t.Fatalf("second library page: %+v", second)
	}

	awaitWeather(t, s, weather.StateOK)
	home := both("/home")
	var card struct {
		Weather asstapp.HomeWeather `json:"weather"`
	}
	if err := json.Unmarshal([]byte(home.body), &card); err != nil {
		t.Fatalf("home has no weather card: %v %.600s", err, home.body)
	}
	if w := card.Weather; w.State != weather.StateOK || w.Location == nil || *w.Location != "Lowmarsh" || w.Current == nil || w.Current.Temperature != 31.5 || w.Current.Condition != "Partly cloudy" || w.Today == nil || w.Today.High != 33 || len(w.Hours) != 2 {
		t.Fatalf("weather card: %+v", w)
	}

	for _, body := range bodies {
		for _, leak := range []string{secret, "dummy-grafana-token", "dummy-anthropic-key", defaultMailPassword} {
			if strings.Contains(body, leak) {
				t.Fatalf("a read model returned a secret value %q: %.300s", leak, body)
			}
		}
	}
}
