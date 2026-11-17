package main

import (
	"net/http"
	"testing"
)

func webProxyOwnerG2(t *testing.T, base string) *webClient {
	t.Helper()
	c := newWebClient(t, base, proxyOwner{http.DefaultTransport})
	c.session("proxy")
	return c
}

func webLocalG2(t *testing.T, base string) *webClient {
	t.Helper()
	c := newWebClient(t, base, nil)
	c.session("local")
	return c
}

func webOKG2(t *testing.T, got apiReply) apiReply {
	t.Helper()
	if got.status < http.StatusOK || got.status >= http.StatusMultipleChoices {
		t.Fatalf("status %d: %.600s", got.status, got.body)
	}
	return got
}

type headersG2 map[string]string

func (h headersG2) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}
