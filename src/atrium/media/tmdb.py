"""TMDB v3: show search for channels, IMDb ids, and posters for the library. None of it is required: without
TMDB_API_KEY the pages work with names only."""

import httpx

from .eztv import imdb_digits

API = "https://api.themoviedb.org/3"
IMAGES = "https://image.tmdb.org/t/p/w342"
TIMEOUT_SECONDS = 15
SEARCH_LIMIT = 8

transport = None  # tests install an httpx.MockTransport


class TmdbError(Exception):
    pass


def from_settings(settings):
    return Tmdb(settings.tmdb_key) if settings.tmdb_key else None


def _year(date):
    return (date or "")[:4]


class Tmdb:
    def __init__(self, key):
        self.key = key

    def _get(self, path, **params):
        try:
            with httpx.Client(timeout=TIMEOUT_SECONDS, transport=transport) as client:
                response = client.get(f"{API}/{path}", params={"api_key": self.key, **params})
        except httpx.HTTPError as e:
            raise TmdbError(f"TMDB unreachable: {type(e).__name__}") from e
        if response.status_code == 404:
            return None
        if response.status_code != 200:
            raise TmdbError(f"TMDB: {response.status_code}")
        try:
            return response.json()
        except ValueError as e:
            raise TmdbError("TMDB: not JSON") from e

    def search_tv(self, query, limit=SEARCH_LIMIT):
        found = (self._get("search/tv", query=query) or {}).get("results") or []
        return [{"tmdb_id": r["id"], "name": r.get("name") or "", "year": _year(r.get("first_air_date")), "poster_path": r.get("poster_path")} for r in found[:limit]]

    def search_movie(self, query, year=""):
        params = {"query": query, **({"year": year} if year else {})}
        return (self._get("search/movie", **params) or {}).get("results") or []

    def imdb_of(self, tmdb_id):
        """IMDb digits of a show, or None when TMDB has none."""
        ids = self._get(f"tv/{int(tmdb_id)}/external_ids") or {}
        return imdb_digits(ids.get("imdb_id"))

    def show_by_imdb(self, imdb):
        found = (self._get(f"find/tt{imdb}", external_source="imdb_id") or {}).get("tv_results") or []
        if not found:
            return None
        r = found[0]
        return {"tmdb_id": r["id"], "name": r.get("name") or "", "year": _year(r.get("first_air_date")), "poster_path": r.get("poster_path")}

    def show(self, tmdb_id):
        return self._get(f"tv/{int(tmdb_id)}")

    def season(self, tmdb_id, number):
        return self._get(f"tv/{int(tmdb_id)}/season/{int(number)}")

    def image(self, poster_path):
        try:
            with httpx.Client(timeout=TIMEOUT_SECONDS, transport=transport) as client:
                response = client.get(IMAGES + poster_path)
        except httpx.HTTPError as e:
            raise TmdbError(f"TMDB images unreachable: {type(e).__name__}") from e
        if response.status_code != 200:
            raise TmdbError(f"TMDB images: {response.status_code}")
        return response.content
