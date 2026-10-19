package money

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const fetchClock = 1792425600.0

func fetchFrom(base string) Fetch {
	return func(ctx context.Context, _ string) (string, error) { return httpx.GetText(ctx, base+"/", 0) }
}

func TestFetchRatesGolden(t *testing.T) {
	d := dbtest.Schema(t)
	loc, err := config.Zone("America/Port_of_Spain")
	if err != nil {
		t.Fatal(err)
	}
	srv := golden.Replay(t, golden.LoadScenario(t, "cbtt", "fx_rates_job").Exchanges)
	var got *FetchedRate
	if err := d.WithTx(t.Context(), func(tx *sql.Tx) error {
		got, err = FetchRates(t.Context(), tx, fetchClock, loc, fetchFrom(srv.URL))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if want := (FetchedRate{Date: "2026-10-19", Selling: 6.7648, AsOf: "2026-10-16"}); *got != want {
		t.Errorf("fetched %+v, want %+v", *got, want)
	}
}

func TestFetchRatesDegraded(t *testing.T) {
	d := dbtest.Schema(t)
	loc, err := config.Zone("America/Port_of_Spain")
	if err != nil {
		t.Fatal(err)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer down.Close()
	ctx := t.Context()
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := FetchRates(ctx, tx, fetchClock, loc, fetchFrom(down.URL))
		return err
	})
	var unavailable *httpx.ServiceUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("fetch error %v, want a ServiceUnavailable", err)
	}
	garbled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>maintenance</html>")) }))
	defer garbled.Close()
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := FetchRates(ctx, tx, fetchClock, loc, fetchFrom(garbled.URL))
		return err
	})
	if !errors.Is(err, httpx.ErrValue) {
		t.Fatalf("fetch error %v, want a ValueError", err)
	}
	var stored int
	if err := d.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM fx_rates").Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("fx_rates holds %d rows after failed fetches (%v)", stored, err)
	}
	if err := d.WithTx(ctx, func(tx *sql.Tx) error {
		id, err := AddAccount(ctx, tx, NewAccount{Bank: Republic, Name: "Card", Kind: CreditCard, Currency: TTD, Last4: "1234", Forex: true}, fetchClock)
		if err != nil {
			return err
		}
		account, err := GetAccount(ctx, tx, id)
		if err != nil {
			return err
		}
		got, err := Resolve(ctx, tx, account, 10, USD, "2026-10-10")
		if err != nil {
			return err
		}
		if got.Amount != nil || got.Review != "no CBTT rate stored yet" {
			t.Errorf("resolve without a rate: %+v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
