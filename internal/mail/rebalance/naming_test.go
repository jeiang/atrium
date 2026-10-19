package rebalance_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeiang/atrium/internal/mail/rebalance"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

func TestSuggestNameSendsTheRecordedRequest(t *testing.T) {
	url := golden.Replay(t, golden.LoadScenario(t, "llm", "rebalance_naming").Exchanges).URL + "/v1"
	got, err := rebalance.SuggestName(url)(context.Background(), "Shopping", []string{"shoply.test", "amazon.test"}, []string{"order", "receipt", "shipped"})
	if err != nil || got != "Receipts" {
		t.Fatalf("SuggestName = %q, %v", got, err)
	}
}

func TestSuggestNameFallsBackWhenTheModelFails(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer down.Close()
	blank := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "{\"name\": \" / | \"}"}}]}`))
	}))
	defer blank.Close()
	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices": [{"message": {}}]}`))
	}))
	defer missing.Close()
	ctx := context.Background()
	for _, c := range []struct {
		url     string
		domains []string
		want    string
	}{
		{down.URL, []string{"Shop/ly.test", "amazon.test"}, "Shop ly.test"},
		{down.URL, nil, "Split"},
		{blank.URL, []string{"shoply.test"}, "shoply.test"},
		{missing.URL, []string{"|"}, "Split"},
	} {
		got, err := rebalance.SuggestName(c.url)(ctx, "Shopping", c.domains, nil)
		if err != nil || got != c.want {
			t.Errorf("SuggestName(%s, %v) = %q, %v; want %q", c.url, c.domains, got, err, c.want)
		}
	}
}

func TestSuggestNameFallsBackOnEmptyOrMalformedReply(t *testing.T) {
	for _, reply := range []string{
		`{"choices": []}`, `{}`, `{"choices": [{"message": {"content": "[]"}}]}`, `{"choices": [{"message": {"content": null}}]}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(reply)) }))
		name := rebalance.SuggestName(srv.URL)
		for _, c := range []struct {
			domains []string
			want    string
		}{{[]string{"shoply.test"}, "shoply.test"}, {nil, "Split"}} {
			if got, err := name(context.Background(), "Shopping", c.domains, []string{"order"}); err != nil || got != c.want {
				t.Errorf("reply %s, domains %v: SuggestName = %q, %v; want %q", reply, c.domains, got, err, c.want)
			}
		}
		srv.Close()
	}
}
