package connections

import (
	"context"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

type builtinConnection struct {
	id    ID
	kind  Kind
	label string
	caps  []Capability
}

var builtinConnections = []builtinConnection{
	{"service/weather", "service/weather", "Open-Meteo", []Capability{{Name: "forecast", Enabled: true}, {Name: "geocoding", Enabled: true}}},
	{"service/webfetch", "service/webfetch", "Web fetch", []Capability{{Name: "egress", Enabled: true}}},
	{"service/tmdb", "service/tmdb", "TMDB", []Capability{{Name: "api", Enabled: true}, {Name: "images", Enabled: true}}},
	{"service/eztv", "service/eztv", "EZTV", []Capability{{Name: "indexer", Enabled: true}}},
	{"service/embedding", "service/embedding", "Embedding", []Capability{{Name: "embeddings", Enabled: true}}},
}

var builtinIDs = func() map[string]bool {
	out := map[string]bool{}
	for _, b := range builtinConnections {
		out[string(b.id)] = true
	}
	return out
}()

func Reconcile(ctx context.Context, d *db.DB, host config.Host) error {
	reg, err := From(ctx)
	if err != nil {
		return err
	}
	store, err := New(d, host, reg)
	if err != nil {
		return err
	}
	if err := store.ReconcileHost(ctx); err != nil {
		return err
	}
	if err := materialize(ctx, store); err != nil {
		return err
	}
	SetDefault(d.Read, store)
	return nil
}

func materialize(ctx context.Context, store *Store) error {
	configs := map[ID]map[string]string{
		"service/weather": {
			"url":           settings.DefaultOf("weather_open_meteo_url"),
			"geocoding_url": settings.DefaultOf("weather_geocoding_url"),
		},
		"service/webfetch": {"url": "https://example.com/"},
		"service/tmdb": {
			"url":        settings.DefaultOf("media_tmdb_url"),
			"images_url": settings.DefaultOf("media_tmdb_images_url"),
		},
		"service/eztv":      {"url": settings.DefaultOf("media_eztv_url")},
		"service/embedding": {"model": settings.DefaultOf("mail_embed_model")},
	}
	for _, b := range builtinConnections {
		if err := store.Ensure(ctx, b.id, b.kind, b.label, configs[b.id], b.caps); err != nil {
			return err
		}
	}
	return nil
}
