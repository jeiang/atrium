"""Release-name parsing and canonical library paths (port of the organizer's parse.go).

Regexes use ASCII classes and boundaries so they match the Go originals.
"""

import re
from dataclasses import dataclass

EPISODE_RE = re.compile(r"\bS(\d{1,2}) ?E(\d{1,3})", re.IGNORECASE | re.ASCII)
YEAR_RE = re.compile(r"\b(19\d{2}|20\d{2})\b", re.ASCII)
LEAD_TAG_RE = re.compile(r"^\s*\[[^\]]*\]\s*", re.ASCII)
SAMPLE_RE = re.compile(r"\bsample\b", re.IGNORECASE | re.ASCII)
UNSAFE = {"/": "", "\\": "", ":": " -", "*": "", "?": "", '"': "", "<": "", ">": "", "|": ""}

VIDEO_EXTS = frozenset({".mkv", ".mp4", ".m4v", ".avi", ".mov", ".ts", ".wmv"})
SUBTITLE_EXTS = frozenset({".srt", ".sub", ".idx", ".ass", ".ssa", ".vtt"})

MOVIES, TV = "movies", "tv"


class ParseError(ValueError):
    pass


@dataclass(frozen=True)
class Release:
    title: str = ""
    year: str = ""
    season: int = 0
    episode: int = 0


def ext(name):
    """Extension from the last dot, like Go's filepath.Ext ('.hidden' is all extension)."""
    i = name.rfind(".")
    return name[i:] if i >= 0 else ""


def is_video(name):
    return ext(name).lower() in VIDEO_EXTS and not SAMPLE_RE.search(name)


def is_subtitle(name):
    return ext(name).lower() in SUBTITLE_EXTS


def parse_release(name):
    """Read a scene-style or "Title (Year)" name; the title is everything before the year or episode marker."""
    if ext(name).lower() in VIDEO_EXTS:
        name = name[: len(name) - len(ext(name))]
    name = LEAD_TAG_RE.sub("", name, count=1)
    name = name.replace(".", " ").replace("_", " ")
    season = episode = 0
    cut = len(name)
    m = EPISODE_RE.search(name)
    if m:
        season, episode = int(m.group(1)), int(m.group(2))
        cut = m.start()
    year = ""
    # The last year before the episode marker; a leading number is the title
    # (2001 A Space Odyssey) and a later one is part of the episode title.
    for m in YEAR_RE.finditer(name, 0, cut):
        if m.start() > 0:
            year, cut = m.group(1), m.start()
    return Release(title=name[:cut].strip(" -(["), year=year, season=season, episode=episode)


def clean_component(text):
    for bad, good in UNSAFE.items():
        text = text.replace(bad, good)
    text = " ".join(text.split()).strip(" .")
    if not text:
        raise ParseError("title is empty")
    return text


def named(title, year):
    title = clean_component(title)
    year = (year or "").strip()
    if not year:
        return title
    if len(year) != 4 or not YEAR_RE.search(year):
        raise ParseError(f"year {year!r} is not a year")
    return f"{title} ({year})"


def movie_path(title, year, extension):
    """Relative to the media root: movies/Title (Year)/Title (Year).ext."""
    n = named(title, year)
    return f"{MOVIES}/{n}/{n}{extension.lower()}"


def episode_path(show, year, season, episode, extension):
    """Relative to the media root: tv/Show (Year)/Season NN/Show (Year) - SNNEMM.ext."""
    n = named(show, year)
    if season < 0 or episode < 1:
        raise ParseError(f"season {season} episode {episode} is not an episode")
    return f"{TV}/{n}/Season {season:02d}/{n} - S{season:02d}E{episode:02d}{extension.lower()}"
