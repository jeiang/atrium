"""Read-only view of movies/ and tv/ under the media root, in the layout parse.py files them into."""

import os
import re
from pathlib import Path

from . import parse

SEASON_DIR = re.compile(r"^Season (\d{1,3})$")
EPISODE_MARK = re.compile(r" - S(\d{2})E(\d{2,3})$")
SEARCH_LIMIT = 50
CHILD_LIMIT = 500


class NotFound(Exception):
    pass


def _dirs(path):
    try:
        return sorted(e for e in os.listdir(path) if (path / e).is_dir())
    except FileNotFoundError:
        return []


def _videos(path):
    try:
        return sorted(e for e in os.listdir(path) if parse.is_video(e) and (path / e).is_file())
    except FileNotFoundError:
        return []


def _entry(kind, name):
    r = parse.parse_release(name)
    return {"dir": name, "title": r.title or name, "year": r.year, "kind": kind}


def safe_rel(root, rel):
    """Absolute path of a library file given its root-relative path, or None when it escapes or is not a file."""
    if not rel or rel.startswith("/") or "\0" in rel:
        return None
    parts = Path(rel).parts
    if parts[0] not in (parse.MOVIES, parse.TV) or any(p in ("..", ".") for p in parts):
        return None
    full = Path(root, rel)
    return full if full.is_file() else None


def movies(root):
    return [_entry(parse.MOVIES, d) for d in _dirs(Path(root) / parse.MOVIES)[:CHILD_LIMIT]]


def shows(root):
    return [_entry(parse.TV, d) for d in _dirs(Path(root) / parse.TV)[:CHILD_LIMIT]]


def _file(root, rel):
    full = Path(root, rel)
    try:
        size = full.stat().st_size
    except OSError:
        size = 0
    return {"rel": rel, "name": full.name, "size": size}


def movie(root, name):
    path = Path(root) / parse.MOVIES / name
    if "/" in name or name in (".", "..") or not path.is_dir():
        raise NotFound(name)
    return {**_entry(parse.MOVIES, name), "files": [_file(root, f"{parse.MOVIES}/{name}/{f}") for f in _videos(path)]}


def episode_number(name):
    m = EPISODE_MARK.search(name[: len(name) - len(parse.ext(name))])
    return (int(m.group(1)), int(m.group(2))) if m else (None, None)


def show(root, name):
    path = Path(root) / parse.TV / name
    if "/" in name or name in (".", "..") or not path.is_dir():
        raise NotFound(name)
    seasons = []
    for d in _dirs(path):
        m = SEASON_DIR.match(d)
        episodes = []
        for f in _videos(path / d):
            season, episode = episode_number(f)
            episodes.append({**_file(root, f"{parse.TV}/{name}/{d}/{f}"), "season": season, "episode": episode})
        seasons.append({"dir": d, "number": int(m.group(1)) if m else None, "episodes": episodes})
    return {**_entry(parse.TV, name), "seasons": seasons}


def episode_files_of(root, name):
    tv = Path(root) / parse.TV / name
    return [f"{parse.TV}/{name}/{d}/{f}" for d in _dirs(tv) for f in _videos(tv / d)]


def episode_files(root):
    """Root-relative paths of every episode file, for the thumbnail pass."""
    return [rel for name in _dirs(Path(root) / parse.TV) for rel in episode_files_of(root, name)]


def search(root, query, limit=SEARCH_LIMIT):
    """Movies and episodes whose folder or file name contains every word of the query."""
    words = [w.casefold() for w in (query or "").split()]
    if not words:
        return []
    out = []
    for m in movies(root):
        if all(w in m["dir"].casefold() for w in words):
            out.append({"kind": "movie", "title": m["title"], "year": m["year"], "dir": m["dir"], "files": [f["rel"] for f in movie(root, m["dir"])["files"]]})
    for s in shows(root):
        for rel in episode_files_of(root, s["dir"]):
            hay = f"{s['dir']} {Path(rel).name}".casefold()
            if all(w in hay for w in words):
                out.append({"kind": "episode", "title": s["title"], "year": s["year"], "dir": s["dir"], "files": [rel]})
        if len(out) >= limit:
            break
    return out[:limit]
