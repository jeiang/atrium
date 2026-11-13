package main

import (
	"net/http"
	"net/http/cookiejar"
	"testing"
)

func TestSettingsDropsAdvancedPages(t *testing.T) {
	base := startServe(t)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, path := range []string{"/settings/config", "/settings/config/assistant", "/settings/audit", "/settings/subagents", "/settings/prompts"} {
		if status, _, _ := fetch(t, client, http.MethodGet, base+path, nil); status != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, status)
		}
	}
}
