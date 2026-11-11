package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Error string

func (e Error) Error() string { return string(e) }

type Location struct {
	Name      string  `json:"name"`
	Country   string  `json:"country,omitempty"`
	Admin1    string  `json:"admin1,omitempty"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type Current struct {
	Time          string  `json:"time"`
	Temperature   float64 `json:"temperature_c"`
	Apparent      float64 `json:"apparent_c"`
	Humidity      float64 `json:"humidity"`
	Precipitation float64 `json:"precipitation_mm"`
	WeatherCode   int     `json:"weather_code"`
	Condition     string  `json:"condition"`
	WindSpeed     float64 `json:"wind_kmh"`
	WindDirection float64 `json:"wind_direction"`
	IsDay         bool    `json:"is_day"`
}

type Hour struct {
	Time          string  `json:"time"`
	Temperature   float64 `json:"temperature_c"`
	Precipitation float64 `json:"precipitation_probability"`
	WeatherCode   int     `json:"weather_code"`
	Condition     string  `json:"condition"`
}

type Day struct {
	Date          string  `json:"date"`
	High          float64 `json:"high_c"`
	Low           float64 `json:"low_c"`
	Precipitation float64 `json:"precipitation_probability"`
	WeatherCode   int     `json:"weather_code"`
	Condition     string  `json:"condition"`
	Sunrise       string  `json:"sunrise,omitempty"`
	Sunset        string  `json:"sunset,omitempty"`
}

type Forecast struct {
	Location Location `json:"location"`
	Timezone string   `json:"timezone,omitempty"`
	Current  Current  `json:"current"`
	Hourly   []Hour   `json:"hourly"`
	Daily    []Day    `json:"daily"`
}

type Provider interface {
	Forecast(ctx context.Context, loc Location, days, hours int) (Forecast, error)
	Geocode(ctx context.Context, name string) ([]Location, error)
}

type OpenMeteo struct {
	client       *http.Client
	forecastURL  string
	geocodingURL string
}

func NewOpenMeteo(client *http.Client, forecastURL, geocodingURL string) *OpenMeteo {
	return &OpenMeteo{client: client, forecastURL: forecastURL, geocodingURL: geocodingURL}
}

func (o *OpenMeteo) Forecast(ctx context.Context, loc Location, days, hours int) (Forecast, error) {
	u, err := url.Parse(o.forecastURL)
	if err != nil {
		return Forecast{}, Error("the forecast URL is invalid")
	}
	q := u.Query()
	q.Set("latitude", number(loc.Latitude))
	q.Set("longitude", number(loc.Longitude))
	q.Set("timezone", "auto")
	q.Set("forecast_days", strconv.Itoa(days))
	q.Set("forecast_hours", strconv.Itoa(hours))
	q.Set("current", "temperature_2m,relative_humidity_2m,apparent_temperature,is_day,precipitation,weather_code,wind_speed_10m,wind_direction_10m")
	q.Set("hourly", "temperature_2m,precipitation_probability,weather_code")
	q.Set("daily", "weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max,sunrise,sunset")
	u.RawQuery = q.Encode()
	var payload forecastPayload
	if err := o.get(ctx, u.String(), &payload); err != nil {
		return Forecast{}, err
	}
	out := Forecast{
		Location: Location{Name: loc.Name, Latitude: payload.Latitude, Longitude: payload.Longitude},
		Timezone: payload.Timezone,
		Current: Current{
			Time:          payload.Current.Time,
			Temperature:   value(payload.Current.Temperature),
			Apparent:      value(payload.Current.Apparent),
			Humidity:      value(payload.Current.Humidity),
			Precipitation: value(payload.Current.Precipitation),
			WeatherCode:   integer(payload.Current.WeatherCode),
			Condition:     Condition(integer(payload.Current.WeatherCode)),
			WindSpeed:     value(payload.Current.WindSpeed),
			WindDirection: value(payload.Current.WindDirection),
			IsDay:         integer(payload.Current.IsDay) == 1,
		},
	}
	for i, stamp := range payload.Hourly.Time {
		if i >= hours {
			break
		}
		code := at(payload.Hourly.WeatherCode, i)
		out.Hourly = append(out.Hourly, Hour{
			Time:          stamp,
			Temperature:   atFloat(payload.Hourly.Temperature, i),
			Precipitation: atFloat(payload.Hourly.Precipitation, i),
			WeatherCode:   code,
			Condition:     Condition(code),
		})
	}
	for i, date := range payload.Daily.Time {
		if i >= days {
			break
		}
		code := at(payload.Daily.WeatherCode, i)
		out.Daily = append(out.Daily, Day{
			Date:          date,
			High:          atFloat(payload.Daily.High, i),
			Low:           atFloat(payload.Daily.Low, i),
			Precipitation: atFloat(payload.Daily.Precipitation, i),
			WeatherCode:   code,
			Condition:     Condition(code),
			Sunrise:       atString(payload.Daily.Sunrise, i),
			Sunset:        atString(payload.Daily.Sunset, i),
		})
	}
	if out.Location.Latitude == 0 && out.Location.Longitude == 0 {
		out.Location.Latitude, out.Location.Longitude = loc.Latitude, loc.Longitude
	}
	return out, nil
}

func (o *OpenMeteo) Geocode(ctx context.Context, name string) ([]Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, Error("a place name is required")
	}
	u, err := url.Parse(o.geocodingURL)
	if err != nil {
		return nil, Error("the geocoding URL is invalid")
	}
	q := u.Query()
	q.Set("name", name)
	q.Set("count", "5")
	q.Set("language", "en")
	q.Set("format", "json")
	u.RawQuery = q.Encode()
	var payload geocodePayload
	if err := o.get(ctx, u.String(), &payload); err != nil {
		return nil, err
	}
	out := make([]Location, len(payload.Results))
	for i, r := range payload.Results {
		out[i] = Location{Name: r.Name, Country: r.Country, Admin1: r.Admin1, Latitude: r.Latitude, Longitude: r.Longitude}
	}
	return out, nil
}

func (o *OpenMeteo) get(ctx context.Context, target string, into any) error {
	ctx, cancel := context.WithTimeout(ctx, TuningWeatherTimeoutSeconds*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Error("the request URL is invalid")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return Error("Open-Meteo is unreachable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Error(fmt.Sprintf("Open-Meteo answered %d", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Error("Open-Meteo did not return a complete reply")
	}
	if err := json.Unmarshal(body, into); err != nil {
		return Error("Open-Meteo returned an unexpected reply")
	}
	return nil
}

type forecastPayload struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  string  `json:"timezone"`
	Current   struct {
		Time          string   `json:"time"`
		Temperature   *float64 `json:"temperature_2m"`
		Apparent      *float64 `json:"apparent_temperature"`
		Humidity      *float64 `json:"relative_humidity_2m"`
		Precipitation *float64 `json:"precipitation"`
		WeatherCode   *int     `json:"weather_code"`
		WindSpeed     *float64 `json:"wind_speed_10m"`
		WindDirection *float64 `json:"wind_direction_10m"`
		IsDay         *int     `json:"is_day"`
	} `json:"current"`
	Hourly struct {
		Time          []string   `json:"time"`
		Temperature   []*float64 `json:"temperature_2m"`
		Precipitation []*float64 `json:"precipitation_probability"`
		WeatherCode   []*int     `json:"weather_code"`
	} `json:"hourly"`
	Daily struct {
		Time          []string   `json:"time"`
		WeatherCode   []*int     `json:"weather_code"`
		High          []*float64 `json:"temperature_2m_max"`
		Low           []*float64 `json:"temperature_2m_min"`
		Precipitation []*float64 `json:"precipitation_probability_max"`
		Sunrise       []*string  `json:"sunrise"`
		Sunset        []*string  `json:"sunset"`
	} `json:"daily"`
}

type geocodePayload struct {
	Results []struct {
		Name      string  `json:"name"`
		Country   string  `json:"country"`
		Admin1    string  `json:"admin1"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"results"`
}

func number(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func value(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func integer(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func atFloat(values []*float64, i int) float64 {
	if i < 0 || i >= len(values) {
		return 0
	}
	return value(values[i])
}

func atString(values []*string, i int) string {
	if i < 0 || i >= len(values) || values[i] == nil {
		return ""
	}
	return *values[i]
}

func at(values []*int, i int) int {
	if i < 0 || i >= len(values) {
		return 0
	}
	return integer(values[i])
}

func Condition(code int) string {
	switch code {
	case 0:
		return "Clear sky"
	case 1:
		return "Mainly clear"
	case 2:
		return "Partly cloudy"
	case 3:
		return "Overcast"
	case 45:
		return "Fog"
	case 48:
		return "Depositing rime fog"
	case 51:
		return "Light drizzle"
	case 53:
		return "Moderate drizzle"
	case 55:
		return "Dense drizzle"
	case 56, 57:
		return "Freezing drizzle"
	case 61:
		return "Slight rain"
	case 63:
		return "Moderate rain"
	case 65:
		return "Heavy rain"
	case 66, 67:
		return "Freezing rain"
	case 71:
		return "Slight snow"
	case 73:
		return "Moderate snow"
	case 75:
		return "Heavy snow"
	case 77:
		return "Snow grains"
	case 80:
		return "Slight rain showers"
	case 81:
		return "Moderate rain showers"
	case 82:
		return "Violent rain showers"
	case 85:
		return "Slight snow showers"
	case 86:
		return "Heavy snow showers"
	case 95:
		return "Thunderstorm"
	case 96, 99:
		return "Thunderstorm with hail"
	}
	return "Unknown"
}
