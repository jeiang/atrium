#!/usr/bin/env python3
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from corpus import emit, error, use_source_tree  # noqa: E402

use_source_tree()

from atrium.media import parse  # noqa: E402

RELEASES = [
    "Code.3.2025.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir.mkv",
    "X-Men.First.Class.2011.PROPER.2160p.BluRay.REMUX.HEVC.DTS-HD.MA.5.1-FGT.mkv",
    "The Prodigy (2009) [xa0scu2].mp4",
    "Lanterns.2026.S01E08.1080p.WEB.h264-ETHEL[EZTVx.to].mkv",
    "Lanterns.S01E07.1080p.HEVC.x265-MeGusta[EZTVx.to].mkv",
    "Show.S02E03.Recap.of.1999.720p.mkv",
    "[Group] Some Show S1E12.mkv",
    "2001.A.Space.Odyssey.1968.mkv",
    "2001.A.Space.Odyssey.mkv",
    "1917.2019.1080p.BluRay.x264.mkv",
    "Blade.Runner.2049.2017.2160p.mkv",
    "Show.Name.s03e112.720p.HDTV.mkv",
    "Show Name S03 E04 720p.mkv",
    "Show_Name_S10E01_Title_Part_2.mp4",
    "show.name.S1E1.mkv",
    "Show.Name.S100E01.mkv",
    "Show.Name.S01E1234.mkv",
    "Show.Name.2024.S01E01E02.1080p.mkv",
    "Show.Name.1x02.HDTV.mkv",
    "Movie Title (1999) - Director's Cut.avi",
    "Movie.Title.1899.1080p.mkv",
    "Movie.Title.2100.1080p.mkv",
    "Movie.Title.20255.mkv",
    "Movie.Title.(2010).[1080p].mov",
    "[SubGroup][Another] Anime Title - 05 [1080p].mkv",
    "  [Lead]  Spaced  Title  2015 .mkv",
    "[Only Tag].mkv",
    "Title.2020.sample.mkv",
    "Title.2020.MKV",
    "Title.2020.nfo",
    "Title.2020",
    "Title.2020.srt",
    "Title - 2018 - 720p.m4v",
    "Title.(2018).ts",
    "Title.[2018].wmv",
    "Mission.Impossible.Dead.Reckoning.Part.One.2023.mkv",
    "Amélie.2001.1080p.mkv",
    "Ünïcödé.Shöw.S01E02.mkv",
    "Title\u00a02019.mkv",
    "Title.２０１９.mkv",
    "S01E01.mkv",
    "2019.mkv",
    ".hidden",
    "",
    "no_extension_title_2004",
    "Title.2004.2005.mkv",
    "Title.2004.S01E01.2005.mkv",
    "Title.19999.mkv",
    "Title-1984-.mkv",
    "Title1984.mkv",
]

FILES = [
    "a.MKV", "a.sample.mkv", "a.nfo", "a.srt", "a.mkv", "Sample.mp4", "sampler.mp4", "A.Sample.Of.Love.2020.mkv",
    "x.mp4", "x.m4v", "x.avi", "x.mov", "x.ts", "x.wmv", "x.webm", "x.SUB", "x.idx", "x.ass", "x.ssa", "x.vtt",
    "x.srt.txt", ".hidden", "name", "dir.v1/name", "trailing.", "a.b.c.MKV", "sample", "x_sample_.mkv", "x-sample-.mkv",
]

COMPONENTS = [
    "Heart Eyes", "Mission: Impossible / Part 1", " .. ", "", "   ", "a\\b", 'He said "hi"?', "<Tag>|pipe*", "Title.", ".Title.",
    "Multi   space\ttab\nnewline", "Ends with dot...", "Amélie", "Title\u00a0NBSP", "C:", "::",
]

NAMED = [
    ("Heart Eyes", "2025"), ("Heart Eyes", ""), ("Heart Eyes", None), ("Heart Eyes", " 2025 "), ("X", "20x5"), ("X", "1899"),
    ("X", "2100"), ("X", "202"), ("X", "20255"), ("X", "２０２５"), (" .. ", "2025"), ("Mission: Impossible / Part 1", "1996"),
]

MOVIES = [
    ("Heart Eyes", "2025", ".MKV"), ("Mission: Impossible / Part 1", "", ".mp4"), (" .. ", "", ".mkv"), ("X", "20x5", ".mkv"),
    ("Amélie", "2001", ".Mp4"), ("Title", "1999", ""), ("Title", "1999", ".TS"),
]

EPISODES = [
    ("Lanterns", "2026", 1, 8, ".mkv"), ("X", "", 1, 0, ".mkv"), ("X", "", 0, 1, ".mkv"), ("X", "", -1, 1, ".mkv"),
    ("X", "", 100, 1000, ".MKV"), ("Show: Name", "2010", 2, 3, ".mp4"), ("", "", 1, 1, ".mkv"), ("Show", "abcd", 1, 1, ".mkv"),
]


def call(fn, *args):
    try:
        value = fn(*args)
    except parse.ParseError as e:
        return {"error": error(e)}
    if isinstance(value, parse.Release):
        value = {"title": value.title, "year": value.year, "season": value.season, "episode": value.episode}
    return {"output": value}


def build():
    records = []
    for name in RELEASES:
        records.append({"fn": "parse_release", "input": [name], **call(parse.parse_release, name)})
    for name in FILES:
        for fn in (parse.ext, parse.is_video, parse.is_subtitle):
            records.append({"fn": fn.__name__, "input": [name], **call(fn, name)})
    for text in COMPONENTS:
        records.append({"fn": "clean_component", "input": [text], **call(parse.clean_component, text)})
    for args in NAMED:
        records.append({"fn": "named", "input": list(args), **call(parse.named, *args)})
    for args in MOVIES:
        records.append({"fn": "movie_path", "input": list(args), **call(parse.movie_path, *args)})
    for args in EPISODES:
        records.append({"fn": "episode_path", "input": list(args), **call(parse.episode_path, *args)})
    return records


if __name__ == "__main__":
    emit(
        "media",
        build(),
        {
            "source": "src/atrium/media/parse.py",
            "functions": ["parse_release", "ext", "is_video", "is_subtitle", "clean_component", "named", "movie_path", "episode_path"],
        },
    )
