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

func Configured(ctx context.Context, q db.Querier, name string, timeout time.Duration) (*http.Client, error) {
	o := Options{Timeout: timeout}
	for key, into := range map[string]*string{CAKey(name): &o.CAFile, ProxyKey(name): &o.Proxy} {
		reg := settings.From(ctx)
		if _, known := reg.Lookup(key); !known {
			continue
		}
		found, err := reg.Resolve(ctx, q, key)
		if err != nil {
			return nil, err
		}
		*into = strings.TrimSpace(found.Value)
	}
	return New(o)
}
