package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/db"
)

func TestInboxListsNotificationsAndSettingsKeepsOnlyChannels(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	seedAllFlags(t)
	base := startServeIn(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	if code, body, _ := fetch(t, client, http.MethodGet, base+"/inbox", nil); code != http.StatusOK || !strings.Contains(body, "<h1>Inbox</h1>") || !strings.Contains(body, "No notifications.") {
		t.Fatalf("empty inbox = %d: %.300s", code, body)
	}
	d, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Write.Exec("INSERT INTO notifications (id, ts, title, body, channel, delivered, error) VALUES (7, 1, 'Parcel arrives today', 'Locker 4', 'assistant', 0, 'no push devices')"); err != nil {
		t.Fatal(err)
	}
	code, inbox, _ := fetch(t, client, http.MethodGet, base+"/inbox", nil)
	if code != http.StatusOK {
		t.Fatalf("inbox = %d: %.300s", code, inbox)
	}
	for _, want := range []string{"Parcel arrives today", "Locker 4", `action="/inbox/7/resend"`, "Local only"} {
		if !strings.Contains(inbox, want) {
			t.Errorf("inbox misses %q", want)
		}
	}
	code, settings, _ := fetch(t, client, http.MethodGet, base+"/settings/notifications", nil)
	if code != http.StatusOK || !strings.Contains(settings, "Channels") {
		t.Fatalf("settings = %d: %.300s", code, settings)
	}
	for _, unwanted := range []string{"Parcel arrives today", "/resend"} {
		if strings.Contains(settings, unwanted) {
			t.Errorf("settings still lists %q", unwanted)
		}
	}
	if code, _, _ := fetch(t, client, http.MethodPost, base+"/settings/notifications/7/resend", url.Values{}); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Errorf("old resend route = %d, want gone", code)
	}
	if code, body, _ := fetch(t, client, http.MethodPost, base+"/inbox/99/resend", url.Values{}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "no notification 99") {
		t.Errorf("resend of an unknown notification = %d: %.300s", code, body)
	}
}
