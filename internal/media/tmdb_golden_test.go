package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"reflect"
	"testing"

	"github.com/jeiang/atrium/internal/testutil/golden"
)

func tmdbAgainst(t *testing.T, scenario string) *TMDB {
	t.Helper()
	srv := golden.Replay(t, golden.LoadScenario(t, "tmdb", scenario).Exchanges)
	api, images := TMDBAPI, TMDBImages
	TMDBAPI, TMDBImages = srv.URL+"/3", srv.URL+"/t/p/w342"
	t.Cleanup(func() { TMDBAPI, TMDBImages = api, images })
	client := NewTMDB("k")
	if client == nil {
		t.Fatal("NewTMDB(k) = nil")
	}
	return client
}

func TestTMDBGoldenClientRequests(t *testing.T) {
	client := tmdbAgainst(t, "client_requests")
	ctx := context.Background()

	shows, err := client.SearchTV(ctx, "lanterns", TMDBSearchLimit)
	want := []TVResult{{TmdbID: 1399, Name: "Lanterns", Year: "2024", PosterPath: "/lanterns.jpg"}}
	if err != nil || !reflect.DeepEqual(shows, want) {
		t.Fatalf("SearchTV = %+v, %v", shows, err)
	}
	if shows, err := client.SearchTV(ctx, "Lanterns & Co", TMDBSearchLimit); err != nil || len(shows) != 0 {
		t.Fatalf("SearchTV with no results = %+v, %v", shows, err)
	}
	for _, year := range []string{"2019", ""} {
		movies, err := client.SearchMovie(ctx, "Night Train", year)
		if err != nil || len(movies) != 1 || movies[0].PosterPath != "/train.jpg" {
			t.Fatalf("SearchMovie(%q) = %+v, %v", year, movies, err)
		}
	}
	if digits, ok, err := client.ImdbOf(ctx, 1399); err != nil || !ok || digits != "1234567" {
		t.Fatalf("ImdbOf = %q, %v, %v", digits, ok, err)
	}
	found, err := client.ShowByImdb(ctx, "1234567")
	if err != nil || found == nil || *found != (TVResult{TmdbID: 1399, Name: "Lanterns", Year: "2024"}) {
		t.Fatalf("ShowByImdb = %+v, %v", found, err)
	}
	show, err := client.Show(ctx, 1399)
	if name, _ := show.Get("name"); err != nil || name != "Lanterns" {
		t.Fatalf("Show = %v, %v", show, err)
	}
	season, err := client.Season(ctx, 1399, 1)
	if path, _ := season.Get("poster_path"); err != nil || path != "/lanterns-s1.jpg" {
		t.Fatalf("Season = %v, %v", season, err)
	}
	image, err := client.Image(ctx, "/lanterns-s1.jpg")
	jpeg, _ := base64.StdEncoding.DecodeString("/9j/2wAAAAAAAAAAAAAAAAAAAAD/2Q==")
	if err != nil || !bytes.Equal(image, jpeg) {
		t.Fatalf("Image = %x, %v", image, err)
	}
}

func TestTMDBGoldenNotFoundReadsAsNoResult(t *testing.T) {
	client := tmdbAgainst(t, "not_found")
	ctx := context.Background()
	if show, err := client.Show(ctx, 4040); err != nil || show != nil {
		t.Errorf("Show = %v, %v", show, err)
	}
	if digits, ok, err := client.ImdbOf(ctx, 4040); err != nil || ok || digits != "" {
		t.Errorf("ImdbOf = %q, %v, %v", digits, ok, err)
	}
	if season, err := client.Season(ctx, 1399, 9); err != nil || season != nil {
		t.Errorf("Season = %v, %v", season, err)
	}
}
