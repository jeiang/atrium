package weather

import (
	"github.com/jeiang/atrium/internal/connections"
)

func ImportV2(values map[string]string) connections.ImportService {
	forecast := values[KeyForecastURL]
	if forecast == "" {
		forecast = DefaultForecastURL
	}
	geocoding := values[KeyGeocodingURL]
	if geocoding == "" {
		geocoding = DefaultGeocodingURL
	}
	return connections.ImportService{
		ID:           string(KindWeather),
		Kind:         string(KindWeather),
		Label:        "Open-Meteo",
		Config:       map[string]string{"url": forecast, "geocoding_url": geocoding},
		Transport:    map[string]string{"ca_file": values["open_meteo_ca_file"], "proxy": values["open_meteo_proxy"]},
		Capabilities: []string{"forecast", "geocoding"},
	}
}
