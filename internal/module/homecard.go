package module

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/homecard"
)

type weatherCard struct {
	unit string
	card homecard.WeatherCard
}

func compileWeatherCard(decls []Declaration) (weatherCard, error) {
	owners := claims{}
	var out weatherCard
	for _, d := range decls {
		if d.HomeWeather == nil {
			continue
		}
		if err := owners.take("home card", "weather", d.Name); err != nil {
			return weatherCard{}, err
		}
		out = weatherCard{unit: d.Name, card: d.HomeWeather}
	}
	return out, nil
}

func HomeWeather(ctx context.Context, d *db.DB, now func() float64) homecard.Weather {
	s := SetFrom(ctx)
	if s == nil {
		return homecard.Off()
	}
	snap := s.Current()
	w := s.Catalog().regs.weather
	if snap == nil || w.card == nil || !snap.Enabled(w.unit) {
		return homecard.Off()
	}
	return w.card.Weather(ctx, d, now)
}
