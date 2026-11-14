//go:build !modernc

package main

import (
	"net/http"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/jeiang/atrium/internal/secrets"
)

func TestV1LenientRequestsStayAccepted(t *testing.T) {
	requireCipher(t)
	_, _, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	token := localAPIToken(t, "compat")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}
	d := keyedDB(t, filepath.Join(f.dir, "atrium.db"))
	payload := func(r apiReply) map[string]any {
		t.Helper()
		if data, ok := r.json["data"].(map[string]any); ok {
			return data
		}
		return r.json
	}
	same := func(name string, lenient, explicit apiReply) {
		t.Helper()
		if lenient.status != explicit.status || lenient.code() != explicit.code() {
			t.Fatalf("%s: lenient %d %q %s, explicit %d %q %s", name, lenient.status, lenient.code(), lenient.body, explicit.status, explicit.code(), explicit.body)
		}
	}

	apiData(t, s.do(http.MethodPut, "/api/v1/connections/google/oauth", token, map[string]any{"client_id": "client-id", "client_secret": "client-secret"}), http.StatusOK)
	apiData(t, s.do(http.MethodPut, "/api/v1/connections/google/oauth", token, map[string]any{"client_id": "rotated-id"}), http.StatusOK)
	apiData(t, s.do(http.MethodPut, "/api/v1/connections/google/oauth", token, map[string]any{}), http.StatusOK)
	setup := s.do(http.MethodGet, "/api/v1/connections/google/oauth", token, nil)
	if got := payload(setup); setup.status != http.StatusOK || got["client_id"] != "rotated-id" || got["client_secret_set"] != true {
		t.Fatalf("google client after partial updates: %d %s", setup.status, setup.body)
	}

	for _, body := range []map[string]any{{"enabled": false}, {}} {
		if put := s.do(http.MethodPut, "/api/v1/permissions/phone", token, body); put.status != http.StatusNoContent {
			t.Fatalf("phone policy %v: %d %s", body, put.status, put.body)
		}
	}

	if _, err := d.Write.Exec("INSERT INTO notifications (ts, title, body, channel, delivered) VALUES (1, 'Compat', 'body', 'assistant', 0)"); err != nil {
		t.Fatal(err)
	}
	var notice int64
	if err := d.Read.QueryRow("SELECT id FROM notifications WHERE title = 'Compat'").Scan(&notice); err != nil {
		t.Fatal(err)
	}
	readPath := "/api/v1/inbox/" + strconv.FormatInt(notice, 10) + "/read"
	if put := s.do(http.MethodPut, readPath, token, map[string]any{"read": true}); put.status != http.StatusNoContent {
		t.Fatalf("mark read: %d %s", put.status, put.body)
	}
	if put := s.do(http.MethodPut, readPath, token, map[string]any{}); put.status != http.StatusNoContent {
		t.Fatalf("absent read: %d %s", put.status, put.body)
	}
	var readAt any
	if err := d.Read.QueryRow("SELECT read_at FROM notifications WHERE id = ?", notice).Scan(&readAt); err != nil || readAt != nil {
		t.Fatalf("absent read left read_at %v %v", readAt, err)
	}

	for _, body := range []any{map[string]any{}, map[string]any{"value": nil}} {
		reply := s.do(http.MethodPut, "/api/v1/settings/calendar_account", token, body)
		apiData(t, reply, http.StatusOK)
		same("setting value", reply, s.do(http.MethodPut, "/api/v1/settings/calendar_account", token, map[string]any{"value": ""}))
	}
	secretPath := "/api/v1/settings/secrets/" + secrets.ActionLinkKey
	same("secret value", s.do(http.MethodPut, secretPath, token, map[string]any{}), s.do(http.MethodPut, secretPath, token, map[string]any{"value": ""}))

	retracted := s.do(http.MethodPost, "/api/v1/knowledge/retract-run", token, map[string]any{})
	apiData(t, retracted, http.StatusOK)
	same("retract run", retracted, s.do(http.MethodPost, "/api/v1/knowledge/retract-run", token, map[string]any{"run_id": 0}))

	created := apiData(t, s.do(http.MethodPost, "/api/v1/knowledge", token, map[string]any{"title": "Compat", "body": "Spaced scope.", "scope": " work"}), http.StatusCreated)
	record := apiData(t, s.do(http.MethodGet, "/api/v1/knowledge/"+strconv.FormatInt(number(t, created["id"]), 10), token, nil), http.StatusOK)["record"].(map[string]any)
	if record["scope"] != "work" {
		t.Fatalf("spaced scope stored as %v", record["scope"])
	}
	apiData(t, s.do(http.MethodPost, "/api/v1/knowledge/confirm-matching", token, map[string]any{"state": " imported "}), http.StatusOK)
}
