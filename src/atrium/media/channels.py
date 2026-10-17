"""Channels: one show (IMDb id) plus a regex over EZTV release titles (port of the organizer's watch.go).

Polling adds every new match to qBittorrent tagged `media` and notifies once per poll. A new channel's first poll
only records what already exists (priming); seen hashes expire after 90 days.
"""

import json
import logging
import re
import time

from ..agent.tools import notify
from . import eztv, organizer
from .qbit import TAG_MEDIA, QbitError
from .settings import DOWNLOADS_PAGE

log = logging.getLogger("atrium.media")

SEEN_TTL = 90 * 86400
NAME_CHARS = 120
PATTERN_CHARS = 300
LAST_CHECK, LAST_ERRORS = "last_check", "last_errors"

SCHEMA = """
CREATE TABLE IF NOT EXISTS media_channels (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    imdb TEXT NOT NULL,
    tmdb_id INTEGER,
    pattern TEXT NOT NULL,
    case_sensitive INTEGER NOT NULL DEFAULT 0,
    primed INTEGER NOT NULL DEFAULT 0,
    created REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS media_seen (
    hash TEXT PRIMARY KEY,
    ts REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS media_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
"""


class ChannelError(Exception):
    pass


def init(conn):
    conn.executescript(SCHEMA)


def compile_pattern(pattern, case_sensitive):
    pattern = (pattern or "").strip()
    if not pattern:
        raise ChannelError("pattern is empty")
    if len(pattern) > PATTERN_CHARS:
        raise ChannelError(f"pattern is longer than {PATTERN_CHARS} characters")
    try:
        return re.compile(pattern, 0 if case_sensitive else re.IGNORECASE)
    except re.error as e:
        raise ChannelError(f"pattern {pattern!r}: {e}") from e


def _row(r):
    return {**dict(r), "case_sensitive": bool(r["case_sensitive"]), "primed": bool(r["primed"])}


def list_channels(conn):
    return [_row(r) for r in conn.execute("SELECT * FROM media_channels ORDER BY id")]


def get_channel(conn, channel_id):
    r = conn.execute("SELECT * FROM media_channels WHERE id = ?", (channel_id,)).fetchone()
    return _row(r) if r else None


def add_channel(conn, name, imdb, pattern, case_sensitive=False, tmdb_id=None, now=time.time, primed=False):
    name = " ".join((name or "").split())[:NAME_CHARS]
    digits = eztv.imdb_digits(imdb)
    if not name:
        raise ChannelError("name is empty")
    if digits is None:
        raise ChannelError(f"IMDb ID {imdb!r} is not tt followed by 7 to 9 digits")
    compile_pattern(pattern, case_sensitive)
    cur = conn.execute(
        "INSERT INTO media_channels (name, imdb, tmdb_id, pattern, case_sensitive, primed, created) VALUES (?, ?, ?, ?, ?, ?, ?)",
        (name, digits, tmdb_id, pattern.strip(), int(bool(case_sensitive)), int(bool(primed)), now()),
    )
    return get_channel(conn, cur.lastrowid)


def delete_channel(conn, channel_id):
    row = get_channel(conn, channel_id)
    if row is None:
        raise ChannelError(f"channel {channel_id} does not exist")
    conn.execute("DELETE FROM media_channels WHERE id = ?", (channel_id,))
    return row


def restore_channel(conn, row):
    """Re-insert a deleted channel with its old id (undo); refuses when the id was reused."""
    if get_channel(conn, row["id"]) is not None:
        raise ChannelError(f"channel {row['id']} exists again")
    conn.execute(
        "INSERT INTO media_channels (id, name, imdb, tmdb_id, pattern, case_sensitive, primed, created) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
        (row["id"], row["name"], row["imdb"], row["tmdb_id"], row["pattern"], int(row["case_sensitive"]), int(row["primed"]), row["created"]),
    )
    return get_channel(conn, row["id"])


def preview(releases, pattern, case_sensitive=False):
    """Each release with `match` set, for the live preview before a channel is saved."""
    regex = compile_pattern(pattern, case_sensitive)
    return [{**t, "match": bool(regex.search(t["title"]))} for t in releases]


def is_seen(conn, hash_):
    return conn.execute("SELECT 1 FROM media_seen WHERE hash = ?", (hash_,)).fetchone() is not None


def mark_seen(conn, hash_, ts):
    conn.execute("INSERT OR REPLACE INTO media_seen (hash, ts) VALUES (?, ?)", (hash_, ts))


def expire_seen(conn, now):
    conn.execute("DELETE FROM media_seen WHERE ts < ?", (now - SEEN_TTL,))


def _set_state(conn, key, value):
    conn.execute("INSERT OR REPLACE INTO media_state (key, value) VALUES (?, ?)", (key, json.dumps(value)))


def status(conn):
    rows = {r["key"]: json.loads(r["value"]) for r in conn.execute("SELECT key, value FROM media_state")}
    return {"last_check": rows.get(LAST_CHECK), "errors": rows.get(LAST_ERRORS) or []}


def gb(size_bytes):
    return f" ({size_bytes / 1e9:.1f} GB)" if size_bytes > 0 else ""


def poll(conn, env, qb, settings, *, now=time.time, fetch=eztv.fetch):
    """One pass over every channel; returns what was added and the errors, which are also kept for the page."""
    stamp = now()
    added, errors = [], []
    for rule in list_channels(conn):
        try:
            regex = compile_pattern(rule["pattern"], rule["case_sensitive"])
            releases = fetch(rule["imdb"])
        except (ChannelError, eztv.EztvError) as e:
            errors.append(f"{rule['name']}: {e}")
            continue
        for t in releases:
            if is_seen(conn, t["hash"]) or not regex.search(t["title"]):
                continue
            if rule["primed"]:
                try:
                    qb.add_magnet(t["magnet_url"], tags=TAG_MEDIA)
                except QbitError as e:
                    errors.append(f"{rule['name']}: {t['title']}: {e}")
                    continue  # retry on the next poll
                added.append({"channel": rule["name"], "title": t["title"], "hash": t["hash"], "size_bytes": t["size_bytes"]})
            mark_seen(conn, t["hash"], stamp)
        conn.execute("UPDATE media_channels SET primed = 1 WHERE id = ?", (rule["id"],))
    expire_seen(conn, stamp)
    if added:
        lines = [f"{a['channel']}: {a['title']}{gb(a['size_bytes'])}" for a in added]
        title = f"Added {added[0]['channel']}" if len(added) == 1 else f"Added {len(added)} releases"
        sent = notify.send(conn, env, title, "\n".join(lines), now=lambda: stamp, topic=settings.ntfy_topic or None, click=settings.public_url + DOWNLOADS_PAGE if settings.public_url else None)
        if not sent["delivered"]:
            errors.append(f"ntfy: {sent['error']}")
        organizer.select_files(qb)
    _set_state(conn, LAST_CHECK, stamp)
    _set_state(conn, LAST_ERRORS, errors)
    for line in errors:
        log.warning("channels: %s", line)
    return {"added": added, "errors": errors}


def import_state(conn, document, now=time.time):
    """Load the organizer's state.json (rules and seen hashes). Go regexes were case-sensitive, so imported rules are too."""
    if not isinstance(document, dict):
        raise ChannelError("state must be a JSON object")
    existing = {(c["imdb"], c["pattern"]) for c in list_channels(conn)}
    channels = 0
    for rule in document.get("rules") or []:
        if not isinstance(rule, dict):
            raise ChannelError("rules must be objects")
        key = (eztv.imdb_digits(rule.get("imdb")), (rule.get("pattern") or "").strip())
        if key in existing:
            continue
        add_channel(conn, rule.get("name"), rule.get("imdb"), rule.get("pattern"), case_sensitive=True, now=now, primed=bool(rule.get("primed")))
        existing.add(key)
        channels += 1
    seen = document.get("seen") or {}
    if not isinstance(seen, dict):
        raise ChannelError("seen must be an object of hash -> unix time")
    for hash_, ts in seen.items():
        if not is_seen(conn, hash_):
            mark_seen(conn, str(hash_).lower(), float(ts))
    expire_seen(conn, now())
    return {"channels": channels, "seen": len(seen)}
