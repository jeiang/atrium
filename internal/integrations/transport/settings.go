package transport

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

func CAKey(name string) string    { return name + "_ca_file" }
func ProxyKey(name string) string { return name + "_proxy" }

var failed = make(chan string, 32)

func Failed() <-chan string { return failed }

func Configured(ctx context.Context, q db.Querier, name string, timeout time.Duration) (*http.Client, error) {
	o := Options{Timeout: timeout}
	for key, into := range map[string]*string{CAKey(name): &o.CAFile, ProxyKey(name): &o.Proxy} {
		if _, known := settings.Default.Lookup(key); !known {
			continue
		}
		found, err := settings.Default.Resolve(ctx, q, key, 0)
		if err != nil {
			return nil, err
		}
		*into = strings.TrimSpace(found.Value)
	}
	return New(o)
}

func For(ctx context.Context, q db.Querier, name string, timeout time.Duration) (*http.Client, error) {
	client, err := Configured(ctx, q, name, timeout)
	if err != nil {
		return nil, err
	}
	client.Transport = reporting{name: name, next: client.Transport}
	return client, nil
}

type reporting struct {
	name string
	next http.RoundTripper
}

func (r reporting) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.next.RoundTrip(req)
	if err != nil && req.Context().Err() == nil || err == nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode >= 500) {
		select {
		case failed <- r.name:
		default:
		}
	}
	return resp, err
}

func (r reporting) CloseIdleConnections() {
	if c, ok := r.next.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
