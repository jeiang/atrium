package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/notify"
)

func TestInboxAndDevicesAPI(t *testing.T) {
	t.Parallel()
	dir, model, _ := moduleFixture(t, flagsExcept("", ""))
	push, browser := pushRig(t, dir)
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	issued := runApp(t, "auth", "token", "issue", "--name", "phone")
	if issued.code != cli.ExitOK {
		t.Fatalf("auth token issue: %d %s", issued.code, issued.stderr)
	}
	token := strings.TrimSpace(issued.stdout)
	serve := func() (*apiSite, func()) {
		t.Helper()
		base, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, "--auth", "local")
		if err != nil {
			t.Fatal(err)
		}
		return &apiSite{t: t, base: base, dir: dir, api: model}, stop
	}
	s, stop := serve()
	t.Cleanup(func() { stop() })
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := d.Read.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	form := browser.form()
	device := map[string]any{"endpoint": form.Get("endpoint"), "p256dh": form.Get("p256dh"), "auth": form.Get("auth"), "label": "Phone"}
	with := func(key string, value any) map[string]any {
		out := map[string]any{}
		for k, v := range device {
			out[k] = v
		}
		out[key] = value
		return out
	}

	apns := s.do(http.MethodPost, "/api/v1/devices", token, with("transport", "apns"))
	expectAPI(t, apns, http.StatusUnprocessableEntity, "invalid")
	if !strings.Contains(apns.body, "unsupported_transport") {
		t.Fatalf("apns refusal %s", apns.body)
	}
	plain := s.do(http.MethodPost, "/api/v1/devices", token, with("endpoint", "http://push.example.test/device"))
	if plain.status != http.StatusUnprocessableEntity || !strings.Contains(plain.body, "push endpoint must be an https URL") {
		t.Fatalf("http endpoint = %d %s", plain.status, plain.body)
	}
	expectAPI(t, plain, http.StatusUnprocessableEntity, "invalid")
	for _, field := range []string{"p256dh", "auth"} {
		bad := s.do(http.MethodPost, "/api/v1/devices", token, with(field, "invalid"))
		expectAPI(t, bad, http.StatusUnprocessableEntity, "invalid")
		if !strings.Contains(bad.body, field) {
			t.Fatalf("missing field %s: %s", field, bad.body)
		}
	}
	if n := count("SELECT COUNT(*) FROM notification_devices"); n != 0 {
		t.Fatalf("%d devices after refused registrations", n)
	}
	reg := s.do(http.MethodPost, "/api/v1/devices", token, with("transport", "webpush"))
	if reg.status != http.StatusCreated {
		t.Fatalf("register = %d %s", reg.status, reg.body)
	}
	deviceID := number(t, reg.json["id"])
	settings := s.do(http.MethodGet, "/api/v1/notifications", token, nil)
	pushCfg, _ := settings.json["push"].(map[string]any)
	devices, _ := pushCfg["devices"].([]any)
	if settings.status != http.StatusOK || len(devices) != 1 || pushCfg["key"] == "" {
		t.Fatalf("configuration = %d %s", settings.status, settings.body)
	}
	if first := devices[0].(map[string]any); number(t, first["id"]) != deviceID || first["transport"] != "webpush" || first["label"] != "Phone" {
		t.Fatalf("device %v", first)
	}
	etag := settings.header.Get("ETag")
	if again := s.do(http.MethodGet, "/api/v1/notifications", token, nil, "If-None-Match", etag); etag == "" || again.status != http.StatusNotModified {
		t.Fatalf("conditional configuration = %d (etag %q)", again.status, etag)
	}
	if put := s.do(http.MethodPut, "/api/v1/notifications/channels/system", token, map[string]any{"content": "title"}); put.status != http.StatusNoContent {
		t.Fatalf("channel content = %d %s", put.status, put.body)
	}
	expectAPI(t, s.do(http.MethodPut, "/api/v1/notifications/channels/system", token, map[string]any{"content": "everything"}), http.StatusConflict, "refused")
	if again := s.do(http.MethodGet, "/api/v1/notifications", token, nil, "If-None-Match", etag); again.status != http.StatusOK || !strings.Contains(again.body, `"content":"title"`) {
		t.Fatalf("configuration after channel change = %d %s", again.status, again.body)
	}

	clearBootNotices(t, d)
	for i := 1; i <= 120; i++ {
		if _, err := d.Write.Exec("INSERT INTO notifications (ts, title, body, channel, delivered) VALUES (?, ?, 'body', 'assistant', 0)", i, fmt.Sprintf("Notice %03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	page := s.do(http.MethodGet, "/api/v1/inbox?limit=100", token, nil)
	items, _ := page.json["items"].([]any)
	cursor, _ := page.json["next_cursor"].(string)
	if page.status != http.StatusOK || len(items) != 100 || cursor == "" {
		t.Fatalf("first page = %d %d items, cursor %q", page.status, len(items), cursor)
	}
	top := items[0].(map[string]any)
	if _, has := top["actions"]; has || top["read_at"] != nil {
		t.Fatalf("inbox item %v", top)
	}
	rest := s.do(http.MethodGet, "/api/v1/inbox?limit=100&cursor="+cursor, token, nil)
	items, _ = rest.json["items"].([]any)
	if rest.status != http.StatusOK || len(items) != 20 || rest.json["next_cursor"] != nil || number(t, items[19].(map[string]any)["id"]) != 1 {
		t.Fatalf("second page = %d %s", rest.status, rest.body)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/inbox?limit=100&channel=system&cursor="+cursor, token, nil), http.StatusBadRequest, "bad_cursor")
	legacy := base64.RawURLEncoding.EncodeToString([]byte(`{"id":21}`))
	malformed := base64.RawURLEncoding.EncodeToString([]byte(`{"id":21}{"id":2}`))
	expectAPI(t, s.do(http.MethodGet, "/api/v1/inbox?cursor="+malformed, token, nil), http.StatusBadRequest, "bad_cursor")
	old := s.do(http.MethodGet, "/api/v1/inbox?limit=100&cursor="+legacy, token, nil)
	oldItems, _ := old.json["items"].([]any)
	if old.status != http.StatusOK || len(oldItems) != 20 || number(t, oldItems[0].(map[string]any)["id"]) != 20 {
		t.Fatalf("old cursor: %d %s", old.status, old.body)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/inbox/999999/resend", token, nil), http.StatusNotFound, "not_found")

	newest := number(t, top["id"])
	readPath := "/api/v1/inbox/" + strconv.FormatInt(newest, 10) + "/read"
	inboxTag := page.header.Get("ETag")
	expectAPI(t, s.do(http.MethodPut, "/api/v1/inbox/999999/read", token, map[string]any{"read": true}), http.StatusNotFound, "not_found")
	if put := s.do(http.MethodPut, readPath, token, map[string]any{"read": true}); put.status != http.StatusNoContent {
		t.Fatalf("mark read = %d %s", put.status, put.body)
	}
	if again := s.do(http.MethodGet, "/api/v1/inbox?limit=100", token, nil, "If-None-Match", inboxTag); again.status != http.StatusOK || again.header.Get("ETag") == inboxTag {
		t.Fatalf("inbox after mark = %d", again.status)
	}
	stop()
	s, stop = serve()
	readAt := func() any {
		t.Helper()
		got := s.do(http.MethodGet, "/api/v1/inbox?limit=1", token, nil)
		items, _ := got.json["items"].([]any)
		if got.status != http.StatusOK || len(items) != 1 {
			t.Fatalf("inbox = %d %s", got.status, got.body)
		}
		return items[0].(map[string]any)["read_at"]
	}
	if readAt() == nil {
		t.Fatal("read_at did not survive a restart")
	}
	if put := s.do(http.MethodPut, readPath, token, map[string]any{"read": false}); put.status != http.StatusNoContent || readAt() != nil {
		t.Fatalf("mark unread = %d", put.status)
	}

	rows := count("SELECT COUNT(*) FROM notifications")
	before := push.count()
	resend := s.do(http.MethodPost, "/api/v1/inbox/"+strconv.FormatInt(newest, 10)+"/resend", token, nil)
	if resend.status != http.StatusOK || resend.json["delivered"] != true || push.count() != before+1 || count("SELECT COUNT(*) FROM notifications") != rows {
		t.Fatalf("resend = %d %s, %d pushes", resend.status, resend.body, push.count()-before)
	}
	if count("SELECT COUNT(*) FROM notifications WHERE id = ? AND delivered = 1", newest) != 1 {
		t.Fatal("the resent row is not delivered")
	}

	if test := s.do(http.MethodPost, "/api/v1/devices/test", token, nil); test.status != http.StatusOK || number(t, test.json["sent"]) != 1 {
		t.Fatalf("device test = %d %s", test.status, test.body)
	}
	if del := s.do(http.MethodDelete, "/api/v1/devices/"+strconv.FormatInt(deviceID, 10), token, nil); del.status != http.StatusNoContent {
		t.Fatalf("remove device = %d %s", del.status, del.body)
	}
	if n := count("SELECT COUNT(*) FROM notification_devices"); n != 0 {
		t.Fatalf("%d devices after removal", n)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/devices/test", token, nil), http.StatusConflict, "refused")
}

func TestDevicesCapAndCredentialRevocation(t *testing.T) {
	t.Parallel()
	dir, model, _ := moduleFixture(t, flagsExcept("", ""))
	push, browser := pushRig(t, dir)
	phone := newSignedInSite(t, dir)
	subscribeG5(t, phone.base, browser)
	issued := runApp(t, "auth", "token", "issue", "--name", "devices")
	if issued.code != cli.ExitOK {
		t.Fatal(issued.stderr)
	}
	token := strings.TrimSpace(issued.stdout)
	s := &apiSite{t: t, base: phone.base, dir: dir, api: model}
	form := browser.form()
	for i := 1; i <= notify.DeviceLimit; i++ {
		got := s.do(http.MethodPost, "/api/v1/devices", token, map[string]any{"endpoint": fmt.Sprintf("%s/push/%d", push.URL, i), "p256dh": form.Get("p256dh"), "auth": form.Get("auth")},
			"User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0")
		if i < notify.DeviceLimit {
			if got.status != http.StatusCreated {
				t.Fatalf("register %d: %d %s", i, got.status, got.body)
			}
		} else {
			expectAPI(t, got, http.StatusConflict, "refused")
		}
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	var label string
	if err := d.Read.QueryRow("SELECT label FROM notification_devices ORDER BY id DESC LIMIT 1").Scan(&label); err != nil || label != "Firefox on Linux" {
		t.Fatalf("label from the user agent = %q: %v", label, err)
	}
	var id int64
	if err := d.Read.QueryRow("SELECT id FROM auth_bearers WHERE name = 'devices'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if got := runApp(t, "auth", "token", "revoke", strconv.FormatInt(id, 10)); got.code != cli.ExitOK {
		t.Fatal(got.stderr)
	}
	var left, owned int
	if err := d.Read.QueryRow("SELECT COUNT(*), COUNT(bearer_id) FROM notification_devices").Scan(&left, &owned); err != nil || left != 1 || owned != 0 {
		t.Fatalf("after revoke devices %d owned %d: %v", left, owned, err)
	}
}
