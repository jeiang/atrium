"""Posters from TMDB and episode thumbnails from ffmpeg, cached under <cache>/media. A miss is remembered with a
`.none` marker for a week so a missing poster does not cost a lookup per page view."""

import logging
import os
import subprocess
import threading
import time
from pathlib import Path

from . import library, parse, tmdb

log = logging.getLogger("atrium.media")

POSTERS, THUMBS = "posters", "thumbs"
MOVIE, SHOW, SEASON = "movie", "show", "season"
KINDS = (MOVIE, SHOW, SEASON)
NONE_TTL = 7 * 86400
THUMB_WIDTH = 480
THUMB_AT = 0.10
NICE = 10
FFMPEG_TIMEOUT = 120
PASS_LIMIT = 200
IMAGE_TYPE = "image/jpeg"

runner = subprocess.run  # tests replace this
_ffmpeg_lock = threading.Lock()  # one frame grab at a time, across requests and the background pass


def _slug(text):
    return "".join(c if c.isalnum() or c in " ()-_." else "_" for c in text).strip() or "_"


def poster_file(cache, kind, name, season=None):
    base = Path(cache) / POSTERS / kind / _slug(name)
    return base / f"{int(season):02d}.jpg" if kind == SEASON else base.with_suffix(".jpg")


def _fresh_none(path):
    marker = path.with_suffix(".none")
    try:
        return time.time() - marker.stat().st_mtime < NONE_TTL
    except FileNotFoundError:
        return False


def _remember_none(path):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.with_suffix(".none").touch()


def _store(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(".tmp")
    tmp.write_bytes(data)
    os.replace(tmp, path)
    return path


def _lookup_poster_path(client, kind, name, season):
    r = parse.parse_release(name)
    if kind == MOVIE:
        found = client.search_movie(r.title or name, r.year)
        return found[0].get("poster_path") if found else None
    shows = client.search_tv(r.title or name, limit=5)
    if r.year:
        shows = [s for s in shows if s["year"] == r.year] or shows
    if not shows:
        return None
    if kind == SHOW:
        return shows[0]["poster_path"]
    detail = client.season(shows[0]["tmdb_id"], season)
    return (detail or {}).get("poster_path")


def poster(cache, client, kind, name, season=None):
    """Cached poster file for a movie dir, show dir or season, fetching once from TMDB; None when unavailable."""
    path = poster_file(cache, kind, name, season)
    if path.is_file():
        return path
    if client is None or _fresh_none(path):
        return None
    try:
        poster_path = _lookup_poster_path(client, kind, name, season)
        if not poster_path:
            _remember_none(path)
            return None
        return _store(path, client.image(poster_path))
    except tmdb.TmdbError as e:
        log.warning("poster %s %s: %s", kind, name, e)
        return None


def thumb_file(cache, rel):
    return Path(cache) / THUMBS / (rel + ".jpg")


NICED = ["nice", "-n", str(NICE)]


def _duration(full):
    out = runner(
        [*NICED, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", str(full)],
        capture_output=True, text=True, timeout=FFMPEG_TIMEOUT,
    )
    try:
        return float(out.stdout.strip()) if out.returncode == 0 else 0.0
    except ValueError:
        return 0.0


def thumbnail(cache, root, rel):
    """Cached frame at ~10% of the episode; generated under a process-wide lock. None when ffmpeg fails."""
    full = library.safe_rel(root, rel)
    if full is None:
        return None
    path = thumb_file(cache, rel)
    if path.is_file():
        return path
    if _fresh_none(path):
        return None
    with _ffmpeg_lock:
        if path.is_file():
            return path
        try:
            at = max(0.0, _duration(full) * THUMB_AT)
            path.parent.mkdir(parents=True, exist_ok=True)
            tmp = path.with_suffix(".tmp.jpg")
            out = runner(
                [*NICED, "ffmpeg", "-v", "error", "-y", "-ss", f"{at:.2f}", "-i", str(full), "-frames:v", "1", "-vf", f"scale={THUMB_WIDTH}:-2", "-q:v", "4", str(tmp)],
                capture_output=True, text=True, timeout=FFMPEG_TIMEOUT,
            )
            if out.returncode != 0 or not tmp.is_file():
                log.warning("thumbnail %s: %s", rel, (out.stderr or "").strip()[:200])
                _remember_none(path)
                return None
            os.replace(tmp, path)
            return path
        except (OSError, subprocess.SubprocessError) as e:
            log.warning("thumbnail %s: %s", rel, e)
            _remember_none(path)
            return None


def thumbnail_pass(cache, root, limit=PASS_LIMIT):
    """Generate missing episode thumbnails, at most `limit` per run."""
    made = skipped = 0
    for rel in library.episode_files(root):
        if thumb_file(cache, rel).is_file():
            continue
        if made >= limit:
            skipped += 1
            continue
        if thumbnail(cache, root, rel) is not None:
            made += 1
    return {"generated": made, "remaining": skipped}
