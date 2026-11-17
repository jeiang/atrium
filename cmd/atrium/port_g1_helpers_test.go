package main

import (
	"net/http"
	"testing"
)

func localWebG1(t *testing.T, base string) *webClient {
	t.Helper()
	c := newWebClient(t, base, nil)
	c.session("local")
	return c
}

func webOKG1(t *testing.T, got apiReply) map[string]any {
	t.Helper()
	if got.status != http.StatusOK {
		t.Fatalf("status %d, want 200: %.600s", got.status, got.body)
	}
	return got.json
}
