import re

import httpx

API = "https://eztvx.to/api/get-torrents"
LIMIT = 100
TIMEOUT_SECONDS = 30
IMDB_RE = re.compile(r"^(?:tt)?(\d{7,9})$")

transport = None  # tests install an httpx.MockTransport


class EztvError(Exception):
    pass


def imdb_digits(value):
    """Digits of an IMDb id ('tt1234567' or '1234567'); None when it is not one."""
    m = IMDB_RE.match((value or "").strip())
    return m.group(1) if m else None


def _size(value):
    try:
        return int(float(value or 0))
    except (TypeError, ValueError):
        return 0


def fetch(imdb, limit=LIMIT):
    """Current releases of one show: hash, title, magnet_url, size_bytes, season, episode."""
    try:
        with httpx.Client(timeout=TIMEOUT_SECONDS, transport=transport) as client:
            response = client.get(API, params={"imdb_id": imdb, "limit": str(limit)})
    except httpx.HTTPError as e:
        raise EztvError(f"EZTV unreachable: {type(e).__name__}") from e
    if response.status_code != 200:
        raise EztvError(f"EZTV: {response.status_code}")
    try:
        body = response.json()
    except ValueError as e:
        raise EztvError("EZTV: not JSON") from e
    out = []
    for t in body.get("torrents") or []:
        if not t.get("hash") or not t.get("magnet_url"):
            continue
        out.append({
            "hash": str(t["hash"]).lower(),
            "title": t.get("title") or "",
            "magnet_url": t["magnet_url"],
            "size_bytes": _size(t.get("size_bytes")),
            "season": str(t.get("season") or ""),
            "episode": str(t.get("episode") or ""),
        })
    return out
