"""Filing queue (port of the organizer's main.go): completed `media` torrents and import/ entries, planned into the
library, hardlinked (torrents keep seeding) or moved (imports); the sweep removes filed torrents once qBittorrent
stopped them at the share limit. Also the one-time video/subtitle selection for torrents added through atrium.
"""

import logging
import os
import shutil
from dataclasses import dataclass, field, replace
from pathlib import Path

from . import parse
from .qbit import PRIORITY_NORMAL, PRIORITY_SKIP, STOPPED_DL, STOPPED_UP, TAG_FILED, TAG_IGNORED, TAG_MEDIA, TAG_SELECTED, QbitError, has_tag
from .settings import IMPORT

log = logging.getLogger("atrium.media")

MOVIE, TV = "movie", "tv"
KINDS = (MOVIE, TV)
SKIPPED = "(skipped)"
NOT_THE_MOVIE = "(not the movie)"
IMPORT_PREFIX = IMPORT + "/"
DIR_MODE = 0o2775


class PlanError(Exception):
    pass


@dataclass(frozen=True)
class Item:
    """One finished torrent or one entry under import/; files are absolute paths of videos, sorted."""

    id: str
    name: str
    torrent: bool
    root: str
    files: tuple


@dataclass
class FileView:
    rel: str
    season: str = ""
    episode: str = ""
    dest: str = ""


@dataclass
class View:
    item: Item
    kind: str = MOVIE
    title: str = ""
    year: str = ""
    files: list = field(default_factory=list)
    movie_file: str = ""
    movie_dest: str = ""
    error: str = ""

    @property
    def id(self):
        return self.item.id


def videos_under(root):
    out = []
    for dirpath, _dirs, names in os.walk(root):
        for name in names:
            full = os.path.join(dirpath, name)
            if parse.is_video(name) and os.path.isfile(full) and not os.path.islink(full):
                out.append(full)
    return tuple(sorted(out))


def items(qb, root):
    """Waiting items and the qBittorrent error text, if any (imports are listed even when qBittorrent is down)."""
    found, error = [], None
    try:
        torrents = qb.completed()
    except QbitError as e:
        torrents, error = [], str(e)
    for t in torrents:
        if not has_tag(t, TAG_MEDIA) or has_tag(t, TAG_FILED) or has_tag(t, TAG_IGNORED):
            continue
        found.append(Item(id=t["hash"], name=t["name"], torrent=True, root=t["content_path"], files=videos_under(t["content_path"])))
    import_dir = Path(root) / IMPORT
    if import_dir.is_dir():
        for entry in sorted(os.listdir(import_dir)):
            entry_root = str(import_dir / entry)
            files = videos_under(entry_root)
            if files:
                found.append(Item(id=IMPORT_PREFIX + entry, name=entry, torrent=False, root=entry_root, files=files))
    return found, error


def find(qb, root, item_id):
    found, error = items(qb, root)
    for it in found:
        if it.id == item_id:
            return it
    raise PlanError(error or f"{item_id} is no longer waiting")


def show_year(root, title):
    """Year of an existing tv/ folder for the same show, so a release without a year joins it."""
    tv = Path(root) / parse.TV
    if not tv.is_dir():
        return ""
    for name in sorted(os.listdir(tv)):
        r = parse.parse_release(name)
        if (tv / name).is_dir() and r.year and r.title.casefold() == title.casefold():
            return r.year
    return ""


def largest(files):
    best, size = "", -1
    for f in files:
        try:
            n = os.stat(f).st_size
        except OSError:
            continue
        if n > size:
            best, size = f, n
    return best


def rel(item, path):
    r = os.path.relpath(path, item.root)
    return os.path.basename(path) if r == "." or r.startswith("..") else r


def defaults(item, root):
    r = parse.parse_release(item.name)
    view = View(item=item, kind=MOVIE, title=r.title, year=r.year)
    for f in item.files:
        fr = parse.parse_release(os.path.basename(f))
        fv = FileView(rel=rel(item, f))
        if fr.episode > 0:
            # Episode files name the show; a season pack's name adds "S01 Pack".
            if view.kind == MOVIE and fr.title:
                view.title, view.year = fr.title, fr.year
            view.kind = TV
            fv.season, fv.episode = str(fr.season), str(fr.episode)
        view.files.append(fv)
    if view.kind == TV and not view.year:
        view.year = show_year(root, view.title)
    return view


def plan(view, root):
    """Fill destinations into view and return the (src, dst) operations; an episode row without a number is skipped."""
    ops = []
    if view.kind == MOVIE:
        src = largest(view.item.files)
        try:
            dst = parse.movie_path(view.title, view.year, parse.ext(src))
        except parse.ParseError as e:
            raise PlanError(str(e)) from e
        view.movie_file, view.movie_dest = rel(view.item, src), dst
        ops.append((src, dst))
    else:
        for fv, src in zip(view.files, view.item.files, strict=True):
            if not fv.episode.strip():
                fv.dest = SKIPPED
                continue
            try:
                season, episode = int(fv.season.strip()), int(fv.episode.strip())
            except ValueError as e:
                raise PlanError(f"{fv.rel}: season and episode must be numbers") from e
            try:
                dst = parse.episode_path(view.title, view.year, season, episode, parse.ext(fv.rel))
            except parse.ParseError as e:
                raise PlanError(f"{fv.rel}: {e}") from e
            fv.dest = dst
            ops.append((src, dst))
        if not ops:
            raise PlanError("no episodes to file")
    seen = set()
    for _, dst in ops:
        if dst in seen:
            raise PlanError(f"{dst} is the destination of two files")
        seen.add(dst)
        if os.path.lexists(os.path.join(root, dst)):
            raise PlanError(f"{dst} already exists")
    return ops


def apply(qb, item, ops, root):
    """Hardlink torrent files, so seeding continues, and move imports; empty import entries are removed."""
    for src, dst in ops:
        full = os.path.join(root, dst)
        os.makedirs(os.path.dirname(full), mode=DIR_MODE, exist_ok=True)
        if item.torrent:
            os.link(src, full)
        else:
            os.rename(src, full)
    if item.torrent:
        qb.add_tags(item.id, TAG_FILED)
    elif not videos_under(item.root):
        shutil.rmtree(item.root)


def planned(view, root):
    """A copy of view with destinations filled or its error set, for display."""
    shown = replace(view, files=[replace(f) for f in view.files])
    try:
        plan(shown, root)
    except PlanError as e:
        shown.error = str(e)
    return shown


def edited(item, root, form):
    """Apply the operator's form (kind, title, year, s{i}/e{i}) over the defaults."""
    view = defaults(item, root)
    view.kind = form.get("kind") if form.get("kind") in KINDS else MOVIE
    view.title, view.year = (form.get("title") or "").strip(), (form.get("year") or "").strip()
    for i, fv in enumerate(view.files):
        if f"e{i}" in form:
            fv.season, fv.episode = (form.get(f"s{i}") or "").strip(), (form.get(f"e{i}") or "").strip()
    return view


def ignore(qb, item):
    if not item.torrent:
        raise PlanError("Only torrents can be ignored; delete the import entry instead.")
    qb.add_tags(item.id, TAG_IGNORED)


def sweep(qb):
    """Remove filed torrents that qBittorrent stopped at the share limit, with their files; the library keeps its hardlink."""
    removed, errors = [], []
    try:
        torrents = qb.completed()
    except QbitError as e:
        return {"removed": removed, "errors": [str(e)]}
    for t in torrents:
        if has_tag(t, TAG_FILED) and t.get("state") == STOPPED_UP:
            try:
                qb.delete(t["hash"], files=True)
                removed.append(t["name"])
            except QbitError as e:
                errors.append(f"{t['name']}: {e}")
    for line in errors:
        log.warning("sweep: %s", line)
    return {"removed": removed, "errors": errors}


def wanted(name):
    return parse.is_video(name) or parse.is_subtitle(name)


def select_files(qb, torrents=None):
    """Once metadata is known, keep video and subtitle files and skip the rest in every `media` torrent not yet selected."""
    done = []
    if torrents is None:
        try:
            torrents = qb.torrents(tag=TAG_MEDIA)
        except QbitError:
            return done
    for t in torrents:
        if not has_tag(t, TAG_MEDIA) or has_tag(t, TAG_SELECTED):
            continue
        try:
            files = qb.files(t["hash"])
            if not files:
                continue  # metadata not here yet
            if any(wanted(f["name"]) for f in files):
                qb.set_priority(t["hash"], [f["index"] for f in files if wanted(f["name"]) and f["priority"] == PRIORITY_SKIP], PRIORITY_NORMAL)
                qb.set_priority(t["hash"], [f["index"] for f in files if not wanted(f["name"]) and f["priority"] != PRIORITY_SKIP], PRIORITY_SKIP)
            qb.add_tags(t["hash"], TAG_SELECTED)
            if t.get("state") in (STOPPED_DL, "pausedDL"):
                qb.start(t["hash"])
            done.append(t["hash"])
        except QbitError as e:
            log.warning("select %s: %s", t.get("name"), e)
    return done
