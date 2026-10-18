"""TMDB v3 request goldens: every media.tmdb.Tmdb request, answered by tests/fake_media.py. Output:
testdata/golden/tmdb/ (or argv[1])."""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from wire import httpx_fake, write_slice  # noqa: E402

from atrium.media import tmdb  # noqa: E402
from fake_media import FakeTmdb  # noqa: E402

SLICE = "tmdb"
KEY = "k"


def served(wire):
    fake = FakeTmdb()
    fake.shows[1399] = {"name": "Lanterns", "first_air_date": "2024-04-01", "imdb_id": "tt1234567", "poster_path": "/lanterns.jpg", "seasons": {1: "/lanterns-s1.jpg"}}
    fake.movies.append({"title": "Night Train", "release_date": "2019-06-01", "poster_path": "/train.jpg"})
    handler = httpx_fake(fake.handle)
    wire.serve(tmdb.API, handler)
    wire.serve(tmdb.IMAGES, handler)
    return fake


def client_requests(wire, tmp):
    served(wire)
    client = tmdb.Tmdb(KEY)
    client.search_tv("lanterns")
    client.search_tv("Lanterns & Co", limit=5)
    client.search_movie("Night Train", "2019")
    client.search_movie("Night Train")
    client.imdb_of(1399)
    client.show_by_imdb("1234567")
    client.show(1399)
    client.season(1399, 1)
    client.image("/lanterns-s1.jpg")


def not_found(wire, tmp):
    served(wire)
    client = tmdb.Tmdb(KEY)
    assert client.show(4040) is None
    assert client.imdb_of(4040) is None
    assert client.season(1399, 9) is None


SCENARIOS = [
    ("client_requests", "Every Tmdb request with api_key in the query: search/tv (channel search, artwork with limit 5), search/movie with and without year, tv/{id}/external_ids, find/tt{imdb} with external_source, tv/{id}, tv/{id}/season/{n}, and the w342 poster image.", ["tests/test_media.py::test_channels_page_search_preview_add_remove_poll", "tests/test_media.py::test_library_pages_posters_and_thumbnails", "src/atrium/media/tmdb.py::Tmdb (every method)"], client_requests),
    ("not_found", "404 answers read as no result (None) for show, external ids and season.", ["src/atrium/media/tmdb.py::Tmdb._get (404 branch)"], not_found),
]

NOTES = ["`TMDB_API_KEY` travels as the `api_key` query parameter; the goldens use the fake's key `k`."]


if __name__ == "__main__":
    write_slice(SLICE, "TMDB", __file__, SCENARIOS, "`api_key=k`", NOTES)
