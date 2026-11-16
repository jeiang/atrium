package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const KindWeather connections.Kind = "service/weather"

type connectionConfig struct {
	URL          string `json:"url"`
	GeocodingURL string `json:"geocoding_url"`
}

func Connections() []connections.Definition {
	return []connections.Definition{{
		Kind:     KindWeather,
		Validate: validateConnection,
		Test:     testConnection,
	}}
}

func validateConnection(c connections.Candidate) error {
	var cfg connectionConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return fmt.Errorf("weather config: %w", err)
	}
	if err := httpURL(cfg.URL); err != nil {
		return fmt.Errorf("forecast URL %w", err)
	}
	if err := httpURL(cfg.GeocodingURL); err != nil {
		return fmt.Errorf("geocoding URL %w", err)
	}
	return nil
}

func testConnection(ctx context.Context, lease *connections.Lease, _ []connections.Capability) ([]connections.Result, error) {
	d := lease.DB()
	if d == nil {
		return nil, errors.New("weather test needs a writable database")
	}
	located, err := locationSet(ctx, d.Read)
	if err != nil {
		return nil, err
	}
	if !located {
		return []connections.Result{{Capability: "forecast", State: connections.Unconfigured, Reason: "no weather location is set"}}, nil
	}
	client, err := lease.HTTPRead("url")
	if err != nil {
		return nil, err
	}
	forecastURL, err := lease.Endpoint("url")
	if err != nil {
		return nil, err
	}
	var cfg connectionConfig
	if err := json.Unmarshal(lease.Config(), &cfg); err != nil {
		return nil, err
	}
	provider := NewOpenMeteo(client, forecastURL, strings.TrimSpace(cfg.GeocodingURL))
	forecast, ferr := provider.Forecast(ctx, Location{Latitude: 0, Longitude: 0}, 1, 1)
	matches, gerr := provider.Geocode(ctx, probePlace)
	results := make([]connections.Result, 0, 2)
	switch {
	case ferr != nil:
		results = append(results, connections.Result{Capability: "forecast", State: connections.Failing, Reason: ferr.Error()})
	default:
		data, err := json.Marshal(map[string]string{"condition": forecast.Current.Condition})
		if err != nil {
			return nil, err
		}
		results = append(results, connections.Result{Capability: "forecast", State: connections.OK, Data: data})
	}
	switch {
	case gerr != nil:
		results = append(results, connections.Result{Capability: "geocoding", State: connections.Failing, Reason: gerr.Error()})
	case len(matches) == 0:
		results = append(results, connections.Result{Capability: "geocoding", State: connections.Failing, Reason: "no place matches the probe"})
	default:
		data, err := json.Marshal(map[string]string{"place": matches[0].Name})
		if err != nil {
			return nil, err
		}
		results = append(results, connections.Result{Capability: "geocoding", State: connections.OK, Data: data})
	}
	return results, nil
}

func locationSet(ctx context.Context, q db.Querier) (bool, error) {
	value := map[string]string{}
	for _, key := range []string{KeyLocationName, KeyLatitude, KeyLongitude} {
		found, err := settings.From(ctx).Resolve(ctx, q, key)
		if err != nil {
			return false, err
		}
		value[key] = strings.TrimSpace(found.Value)
	}
	return value[KeyLocationName] != "" || value[KeyLatitude] != "" && value[KeyLongitude] != "", nil
}

const probePlace = "Reykjavik"
