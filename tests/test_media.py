import base64
import hashlib
import json
import os
import re
from urllib.parse import quote

import pytest
from agent_tool_helpers import make_ctx
from fake_media import JPEG, FakeEztv, FakeFfmpeg, FakeNtfy, FakeQbit, FakeTmdb
from fastapi.testclient import TestClient
from helpers import make_config, static_dir, web_client

from atrium import cli, db
from atrium.agent import builtin_jobs, registry, scheduler, subagents, writes
from atrium.agent.tools import media as media_tools
from atrium.agent.tools import notify
from atrium.agent.tools.base import NotConfigured, ToolError
from atrium.media import artwork, channels, eztv, library, links, organizer, parse, qbit, tmdb
from atrium.media import settings as media_settings
from atrium.web import app as web

NOW = 1_800_000_000.0
FAR = 4_000_000_000
HASH_A = "a" * 40
HASH_B = "b" * 40
HASH_C = "c" * 40
GROUP = {"Remote-Groups": "atrium_access"}


def env_for(tmp_path, **extra):
    return {
        "ATRIUM_MEDIA_ROOT": str(tmp_path / "media"),
        "QBITTORRENT_URL": "http://qbit.test",
        "ATRIUM_PUBLIC_URL": "https://atrium.test",
        "ATRIUM_MEDIA_MOVIES_URL": "http://artemis.vpn:8084",
        "ATRIUM_MEDIA_TV_URL": "http://artemis.vpn:8085",
        "NTFY_URL": "https://ntfy.test",
        "NTFY_TOKEN": "tok",
        "NTFY_MEDIA_TOPIC": "media-releases",
        "TMDB_API_KEY": "k",
        "ATRIUM_MEDIA_LINK_KEY": "secret-key",
        **extra,
    }


def torrent_bytes(name="Show.S01E01.1080p"):
    info = b"d4:name" + f"{len(name)}:{name}".encode() + b"12:piece lengthi16384e6:pieces20:" + b"\x00" * 20 + b"e"
    return b"d8:announce9:http://x/4:info" + info + b"e", hashlib.sha1(info).hexdigest()


def media_tree(tmp_path):
    root = tmp_path / "media"
    for d in ("import", "movies", "tv", "downloads"):
        (root / d).mkdir(parents=True, exist_ok=True)
    return root


@pytest.fixture
def world(tmp_path, monkeypatch):
    root = media_tree(tmp_path)
    fake = FakeQbit()
    ez, tm, nt = FakeEztv(), FakeTmdb(), FakeNtfy()
    monkeypatch.setattr(qbit, "transport", fake.transport)
    monkeypatch.setattr(eztv, "transport", ez.transport)
    monkeypatch.setattr(tmdb, "transport", tm.transport)
    monkeypatch.setattr(notify, "transport", nt.transport)
    cfg = make_config(tmp_path, static_dir=static_dir(tmp_path))
    env = env_for(tmp_path)
    conn = db.connect(cfg.db_path)
    conn.execute("INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (1, 'chat', 'low', 0, 0)")
    conn.execute("INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, 1, 'chat', 'm', 'low', 'running', 0)")
    yield {"root": root, "qb": fake, "eztv": ez, "tmdb": tm, "ntfy": nt, "cfg": cfg, "env": env, "conn": conn, "settings": media_settings.load(cfg, env)}
    conn.close()


def completed_media(w, hash_=HASH_A, name="Lanterns.S01E08.1080p.WEB.h264-ETHEL", files=("Lanterns.S01E08.1080p.WEB.h264-ETHEL.mkv", "Sample/sample.mkv", "info.nfo"), tags=("media",)):
    content = w["root"] / "downloads" / name
    content.mkdir(parents=True, exist_ok=True)
    for f in files:
        (content / f).parent.mkdir(parents=True, exist_ok=True)
        (content / f).write_bytes(b"x" * (10 if f.endswith(".nfo") else 100))
    w["qb"].add(hash_, name, tags=tags, state=qbit.STOPPED_UP, content_path=str(content))
    return content


def library_files(root):
    movie = root / "movies/Dune (2021)"
    movie.mkdir(parents=True)
    (movie / "Dune (2021).mkv").write_bytes(b"0123456789" * 10)
    season = root / "tv/Lanterns (2026)/Season 01"
    season.mkdir(parents=True)
    (season / "Lanterns (2026) - S01E08.mkv").write_bytes(b"episode-bytes")
    return "movies/Dune (2021)/Dune (2021).mkv", "tv/Lanterns (2026)/Season 01/Lanterns (2026) - S01E08.mkv"


def signed(key, rel, exp=FAR):
    return f"/media/file/{quote(rel)}?exp={exp}&sig={links.sign(key, rel, exp)}"


# ---------- parsing parity with the Go organizer ----------


@pytest.mark.parametrize(
    "name, want",
    [
        ("Code.3.2025.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir.mkv", parse.Release("Code 3", "2025")),
        ("X-Men.First.Class.2011.PROPER.2160p.BluRay.REMUX.HEVC.DTS-HD.MA.5.1-FGT.mkv", parse.Release("X-Men First Class", "2011")),
        ("The Prodigy (2009) [xa0scu2].mp4", parse.Release("The Prodigy", "2009")),
        ("Lanterns.2026.S01E08.1080p.WEB.h264-ETHEL[EZTVx.to].mkv", parse.Release("Lanterns", "2026", 1, 8)),
        ("Lanterns.S01E07.1080p.HEVC.x265-MeGusta[EZTVx.to].mkv", parse.Release("Lanterns", "", 1, 7)),
        ("Show.S02E03.Recap.of.1999.720p.mkv", parse.Release("Show", "", 2, 3)),
        ("[Group] Some Show S1E12.mkv", parse.Release("Some Show", "", 1, 12)),
        ("2001.A.Space.Odyssey.1968.mkv", parse.Release("2001 A Space Odyssey", "1968")),
    ],
)
def test_parse_release(name, want):
    assert parse.parse_release(name) == want


def test_paths():
    assert parse.movie_path("Heart Eyes", "2025", ".MKV") == "movies/Heart Eyes (2025)/Heart Eyes (2025).mkv"
    assert parse.episode_path("Lanterns", "2026", 1, 8, ".mkv") == "tv/Lanterns (2026)/Season 01/Lanterns (2026) - S01E08.mkv"
    assert parse.movie_path("Mission: Impossible / Part 1", "", ".mp4") == "movies/Mission - Impossible Part 1/Mission - Impossible Part 1.mp4"
    for bad in (lambda: parse.movie_path(" .. ", "", ".mkv"), lambda: parse.movie_path("X", "20x5", ".mkv"), lambda: parse.episode_path("X", "", 1, 0, ".mkv")):
        with pytest.raises(parse.ParseError):
            bad()


def test_video_and_subtitle_detection():
    assert parse.is_video("a.MKV") and not parse.is_video("a.sample.mkv") and not parse.is_video("a.nfo")
    assert parse.is_subtitle("a.srt") and not parse.is_subtitle("a.mkv")
    assert parse.ext(".hidden") == ".hidden" and parse.ext("name") == ""


# ---------- qBittorrent client helpers ----------


def test_magnet_and_torrent_hashes():
    assert qbit.magnet_hash(f"magnet:?xt=urn:btih:{HASH_A.upper()}&dn=x") == HASH_A
    b32 = base64.b32encode(bytes.fromhex(HASH_B)).decode()
    assert qbit.magnet_hash(f"magnet:?dn=y&xt=urn:btih:{b32}") == HASH_B
    assert qbit.magnet_hash("magnet:?dn=only") is None and qbit.magnet_hash("http://x") is None
    data, hash_ = torrent_bytes()
    assert qbit.torrent_hash(data) == hash_
    with pytest.raises(qbit.QbitError):
        qbit.torrent_hash(b"not a torrent")
    assert qbit.has_tag({"tags": "media, filed"}, "filed") and not qbit.has_tag({"tags": ""}, "media")


# ---------- filing ----------


def test_filing_hardlinks_torrent_and_tags_filed(world):
    w = world
    content = completed_media(w)
    qb = qbit.Qbit("http://qbit.test")
    items, error = organizer.items(qb, w["root"])
    assert error is None and [it.id for it in items] == [HASH_A]
    assert [os.path.basename(f) for f in items[0].files] == ["Lanterns.S01E08.1080p.WEB.h264-ETHEL.mkv"]
    view = organizer.defaults(items[0], w["root"])
    assert view.kind == "tv" and view.title == "Lanterns" and view.year == ""
    ops = organizer.plan(view, w["root"])
    assert ops == [(str(content / "Lanterns.S01E08.1080p.WEB.h264-ETHEL.mkv"), "tv/Lanterns/Season 01/Lanterns - S01E08.mkv")]
    organizer.apply(qb, items[0], ops, w["root"])
    dst = w["root"] / "tv/Lanterns/Season 01/Lanterns - S01E08.mkv"
    assert dst.stat().st_ino == (content / "Lanterns.S01E08.1080p.WEB.h264-ETHEL.mkv").stat().st_ino
    assert "filed" in w["qb"].torrents[HASH_A]["tags"]
    assert organizer.items(qb, w["root"])[0] == []
    with pytest.raises(organizer.PlanError, match="already exists"):
        organizer.plan(organizer.defaults(items[0], w["root"]), w["root"])


def test_show_year_joins_existing_folder(world):
    w = world
    (w["root"] / "tv/Lanterns (2026)").mkdir(parents=True)
    completed_media(w)
    items, _ = organizer.items(qbit.Qbit("http://qbit.test"), w["root"])
    view = organizer.defaults(items[0], w["root"])
    assert view.year == "2026"
    assert organizer.plan(view, w["root"])[0][1] == "tv/Lanterns (2026)/Season 01/Lanterns (2026) - S01E08.mkv"


def test_filing_moves_import_and_removes_empty_dir(world):
    w = world
    src = w["root"] / "import/Heart Eyes (2025)"
    src.mkdir(parents=True)
    (src / "Heart.Eyes.2025.1080p.mkv").write_bytes(b"m" * 300)
    (src / "Heart.Eyes.2025.sample.mkv").write_bytes(b"s" * 10)
    qb = qbit.Qbit("http://qbit.test")
    items, _ = organizer.items(qb, w["root"])
    assert [it.id for it in items] == ["import/Heart Eyes (2025)"] and not items[0].torrent
    view = organizer.defaults(items[0], w["root"])
    assert view.kind == "movie" and (view.title, view.year) == ("Heart Eyes", "2025")
    ops = organizer.plan(view, w["root"])
    assert view.movie_file == "Heart.Eyes.2025.1080p.mkv" and ops[0][1] == "movies/Heart Eyes (2025)/Heart Eyes (2025).mkv"
    organizer.apply(qb, items[0], ops, w["root"])
    assert (w["root"] / "movies/Heart Eyes (2025)/Heart Eyes (2025).mkv").read_bytes() == b"m" * 300
    assert not src.exists()


def test_edited_form_overrides_defaults(world):
    w = world
    completed_media(w, files=("ep1.mkv", "ep2.mkv"))
    items, _ = organizer.items(qbit.Qbit("http://qbit.test"), w["root"])
    view = organizer.edited(items[0], w["root"], {"kind": "tv", "title": "Lanterns", "year": "2026", "s0": "1", "e0": "1", "s1": "1", "e1": ""})
    ops = organizer.plan(view, w["root"])
    assert [d for _, d in ops] == ["tv/Lanterns (2026)/Season 01/Lanterns (2026) - S01E01.mkv"]
    assert view.files[1].dest == organizer.SKIPPED
    dup = organizer.edited(items[0], w["root"], {"kind": "tv", "title": "L", "s0": "1", "e0": "1", "s1": "1", "e1": "1"})
    with pytest.raises(organizer.PlanError, match="two files"):
        organizer.plan(dup, w["root"])
    with pytest.raises(organizer.PlanError, match="must be numbers"):
        organizer.plan(organizer.edited(items[0], w["root"], {"kind": "tv", "title": "L", "s0": "", "e0": "1"}), w["root"])


def test_untagged_and_filed_torrents_are_left_alone(world):
    w = world
    completed_media(w, HASH_A, tags=())
    completed_media(w, HASH_B, name="B", tags=("media", "filed"))
    completed_media(w, HASH_C, name="C", tags=("media", "ignored"))
    qb = qbit.Qbit("http://qbit.test")
    assert organizer.items(qb, w["root"])[0] == []
    assert organizer.select_files(qb) == []
    with pytest.raises(organizer.PlanError):
        organizer.ignore(qb, organizer.Item("import/x", "x", False, "x", ()))


def test_sweep_only_after_stopped_up(world):
    w = world
    completed_media(w, HASH_A, tags=("media", "filed"))
    w["qb"].torrents[HASH_A]["state"] = "uploading"
    completed_media(w, HASH_B, name="B", tags=("media", "filed"))
    completed_media(w, HASH_C, name="C", tags=("media",))
    qb = qbit.Qbit("http://qbit.test")
    assert organizer.sweep(qb) == {"removed": ["B"], "errors": []}
    assert w["qb"].deleted == [(HASH_B, True)] and HASH_A in w["qb"].torrents and HASH_C in w["qb"].torrents
    w["qb"].fail = 500
    assert organizer.sweep(qb)["errors"]


def test_select_files_keeps_video_and_subtitles_and_starts(world):
    w = world
    fake = w["qb"]
    fake.add(HASH_A, "pack", tags=("media",), state="stoppedDL", progress=0.0, files=[fake.file(0, "ep.mkv", priority=0), fake.file(1, "ep.srt", priority=0), fake.file(2, "readme.nfo"), fake.file(3, "Sample/sample.mkv")])
    fake.add(HASH_B, "meta pending", tags=("media",), state="metaDL", progress=0.0)
    fake.add(HASH_C, "plain", tags=(), state="downloading", progress=0.0, files=[fake.file(0, "x.exe")])
    qb = qbit.Qbit("http://qbit.test")
    assert organizer.select_files(qb) == [HASH_A]
    assert [f["priority"] for f in fake.torrents[HASH_A]["files"]] == [1, 1, 0, 0]
    assert "selected" in fake.torrents[HASH_A]["tags"] and fake.started == [HASH_A]
    assert "selected" not in fake.torrents[HASH_B]["tags"] and fake.torrents[HASH_C]["files"][0]["priority"] == 1
    assert organizer.select_files(qb) == []  # idempotent


# ---------- channels ----------


def test_channel_validation_and_case_toggle(world):
    conn = world["conn"]
    with pytest.raises(channels.ChannelError, match="IMDb"):
        channels.add_channel(conn, "Lanterns", "123", "x")
    with pytest.raises(channels.ChannelError, match="pattern"):
        channels.add_channel(conn, "Lanterns", "tt1234567", "(")
    with pytest.raises(channels.ChannelError, match="name"):
        channels.add_channel(conn, " ", "tt1234567", "x")
    row = channels.add_channel(conn, "Lanterns", "tt1234567", "^lanterns s\\d+e\\d+ 1080p", now=lambda: NOW)
    assert row["imdb"] == "1234567" and row["case_sensitive"] is False and row["primed"] is False
    releases = [{"hash": "1", "title": "Lanterns S01E08 1080p WEB", "size_bytes": 0}, {"hash": "2", "title": "Lanterns S01E08 720p", "size_bytes": 0}]
    assert [r["match"] for r in channels.preview(releases, row["pattern"], False)] == [True, False]
    assert [r["match"] for r in channels.preview(releases, row["pattern"], True)] == [False, False]
    assert channels.delete_channel(conn, row["id"])["id"] == row["id"]
    with pytest.raises(channels.ChannelError):
        channels.delete_channel(conn, row["id"])


def test_poll_primes_then_adds_tagged_and_notifies(world):
    w = world
    conn, ez, fake = w["conn"], w["eztv"], w["qb"]
    ez.release("1234567", HASH_A, "Lanterns S01E07 1080p WEB h264")
    ez.release("1234567", HASH_B, "Lanterns S01E07 720p")
    channels.add_channel(conn, "Lanterns", "tt1234567", r"^Lanterns S\d+E\d+ 1080p", now=lambda: NOW)
    qb = qbit.Qbit("http://qbit.test")
    first = channels.poll(conn, w["env"], qb, w["settings"], now=lambda: NOW)
    assert first == {"added": [], "errors": []} and fake.added == []
    assert channels.is_seen(conn, HASH_A) and not channels.is_seen(conn, HASH_B)
    assert channels.list_channels(conn)[0]["primed"] is True
    assert channels.status(conn) == {"last_check": NOW, "errors": []}

    ez.release("1234567", HASH_C, "Lanterns S01E08 1080p WEB h264", size="2500000000")
    second = channels.poll(conn, w["env"], qb, w["settings"], now=lambda: NOW + 60)
    assert [a["hash"] for a in second["added"]] == [HASH_C] and second["errors"] == []
    assert fake.added == [{"kind": "magnet", "magnet": f"magnet:?xt=urn:btih:{HASH_C}&dn=x", "hash": HASH_C, "tags": ["media"]}]
    assert "media" in fake.torrents[HASH_C]["tags"]
    sent = w["ntfy"].sent[-1]
    assert sent["topic"] == "media-releases" and sent["click"] == "https://atrium.test/media/downloads"
    assert "Lanterns S01E08 1080p WEB h264 (2.5 GB)" in sent["message"] and "actions" not in sent
    assert conn.execute("SELECT title, delivered FROM notifications").fetchone()[1] == 1
    assert channels.poll(conn, w["env"], qb, w["settings"], now=lambda: NOW + 120)["added"] == []  # seen

    # Seen hashes expire after 90 days, errors are kept for the page, a failing add is retried next poll.
    channels.expire_seen(conn, NOW + channels.SEEN_TTL + 200)
    assert not channels.is_seen(conn, HASH_A)
    fake.fail = 500
    third = channels.poll(conn, w["env"], qb, w["settings"], now=lambda: NOW + 180)
    assert third["added"] == [] and "Lanterns" in third["errors"][0] and not channels.is_seen(conn, HASH_A)
    assert channels.status(conn)["errors"] == third["errors"]
    fake.fail = None
    ez.fail = 503
    assert "EZTV: 503" in channels.poll(conn, w["env"], qb, w["settings"], now=lambda: NOW + 240)["errors"][0]


def test_forced_single_channel_poll_backfills_seen_release_and_keeps_global_state(world):
    w = world
    conn, ez, fake = w["conn"], w["eztv"], w["qb"]
    ez.release("1234567", HASH_A, "Lanterns S01E07 1080p WEB h264")
    ez.release("7654321", HASH_B, "Other S01E01 1080p")
    lanterns = channels.add_channel(conn, "Lanterns", "tt1234567", r"^Lanterns S\d+E\d+ 1080p", now=lambda: NOW)
    channels.add_channel(conn, "Other", "tt7654321", "1080p", now=lambda: NOW)
    qb = qbit.Qbit("http://qbit.test")
    channels.poll(conn, w["env"], qb, w["settings"], now=lambda: NOW)
    assert fake.added == [] and channels.is_seen(conn, HASH_A)
    out = channels.poll(conn, w["env"], qb, w["settings"], now=lambda: NOW + 60, only=lanterns["id"], force=True)
    assert [a["hash"] for a in out["added"]] == [HASH_A]
    assert [a["hash"] for a in fake.added] == [HASH_A]
    assert channels.status(conn) == {"last_check": NOW, "errors": []}


def test_poll_selects_files_of_added_torrents(world):
    w = world
    conn, fake = w["conn"], w["qb"]
    w["eztv"].release("1234567", HASH_A, "Lanterns S01E01 1080p")
    channels.add_channel(conn, "Lanterns", "tt1234567", "1080p", now=lambda: NOW, primed=True)
    fake.add(HASH_A, "Lanterns S01E01", tags=("media",), state="downloading", progress=0.0, files=[fake.file(0, "e.mkv", priority=0), fake.file(1, "e.txt")])
    out = channels.poll(conn, w["env"], qbit.Qbit("http://qbit.test"), w["settings"], now=lambda: NOW)
    assert [a["hash"] for a in out["added"]] == [HASH_A]
    assert [f["priority"] for f in fake.torrents[HASH_A]["files"]] == [1, 0] and "selected" in fake.torrents[HASH_A]["tags"]


def test_cli_poll_and_sweep(world, tmp_path, monkeypatch, capsys):
    w = world
    for k, v in {**w["env"], "ATRIUM_DB": str(w["cfg"].db_path), "ATRIUM_CACHE_DIR": str(w["cfg"].cache_dir)}.items():
        monkeypatch.setenv(k, v)
    channels.add_channel(w["conn"], "Lanterns", "tt1234567", "1080p", now=lambda: NOW, primed=True)
    w["eztv"].release("1234567", HASH_A, "Lanterns S01E01 1080p")
    assert cli.main(["media", "poll"]) == 0
    assert [a["hash"] for a in json.loads(capsys.readouterr().out)["added"]] == [HASH_A]
    completed_media(w, HASH_B, name="B", tags=("media", "filed"))
    assert cli.main(["media", "sweep"]) == 0
    assert json.loads(capsys.readouterr().out)["removed"] == ["B"]


def test_builtin_media_jobs_registered_and_skip_when_unconfigured(world):
    conn = world["conn"]
    assert {builtin_jobs.MEDIA_CHANNELS_NAME, builtin_jobs.MEDIA_SWEEP_NAME, builtin_jobs.MEDIA_THUMBNAILS_NAME} <= set(builtin_jobs.CALLABLES)
    schedules = {b.name: b.schedule for b in builtin_jobs.BUILTIN_JOBS}
    assert schedules[builtin_jobs.MEDIA_CHANNELS_NAME] == "*/30 * * * *" and schedules[builtin_jobs.MEDIA_SWEEP_NAME] == "*/10 * * * *"
    for name in (builtin_jobs.MEDIA_CHANNELS_NAME, builtin_jobs.MEDIA_SWEEP_NAME, builtin_jobs.MEDIA_THUMBNAILS_NAME):
        assert scheduler.parse(schedules[name], "UTC").recurring
    channels.add_channel(conn, "L", "tt1234567", "x", now=lambda: NOW)
    assert "skipped" in builtin_jobs.media_channels(world["cfg"], conn, env={})
    assert "skipped" in builtin_jobs.media_sweep(world["cfg"], conn, env={})
    assert "skipped" in builtin_jobs.media_thumbnails(world["cfg"], conn, env={})
    assert builtin_jobs.media_sweep(world["cfg"], conn, env=world["env"]) == {"selected": [], "removed": [], "errors": []}


# ---------- links ----------


def test_signed_links_valid_expired_tampered(world):
    s = world["settings"]
    key = links.key(s)
    assert key == b"secret-key"
    rel = "movies/Dune (2021)/Dune (2021).mkv"
    url = links.public_url(s, key, rel, now=NOW)
    assert url.startswith("https://atrium.test/media/file/movies/Dune%20%282021%29/Dune%20%282021%29.mkv?exp=")
    exp = int(NOW + media_settings.LINK_TTL_SECONDS)
    sig = links.sign(key, rel, exp)
    assert f"exp={exp}&sig={sig}" in url
    assert links.verify(key, rel, exp, sig, now=NOW)
    assert not links.verify(key, rel, exp, sig, now=exp + 1)
    assert not links.verify(key, rel, exp, sig[:-1] + ("0" if sig[-1] != "0" else "1"), now=NOW)
    assert not links.verify(key, rel + "x", exp, sig, now=NOW)
    assert not links.verify(key, rel, exp + 1, sig, now=NOW)
    assert not links.verify(key, rel, "soon", sig, now=NOW)
    assert links.vpn_url(s, rel) == "http://artemis.vpn:8084/Dune%20%282021%29/Dune%20%282021%29.mkv"
    assert links.vpn_url(s, "tv/A/Season 01/x.mkv") == "http://artemis.vpn:8085/A/Season%2001/x.mkv"
    assert links.vpn_url(s, "downloads/x") == ""


def test_link_key_is_created_once_without_env(tmp_path):
    cfg = make_config(tmp_path)
    s = media_settings.load(cfg, {})
    first = links.key(s)
    assert len(first) == 64 and (tmp_path / "media-link.key").stat().st_mode & 0o777 == 0o600
    assert links.key(media_settings.load(cfg, {})) == first


# ---------- web ----------


def test_media_file_route_serves_ranges_with_valid_signature(world):
    w = world
    movie_rel, _ = library_files(w["root"])
    client = web_client(w["cfg"], env=w["env"])
    key = links.key(w["settings"])
    good = signed(key, movie_rel)
    full = client.get(good)
    assert full.status_code == 200 and full.content == b"0123456789" * 10 and full.headers["content-type"].startswith("video/x-matroska")
    assert full.headers.get("accept-ranges") == "bytes"
    part = client.get(good, headers={"Range": "bytes=10-19"})
    assert part.status_code == 206 and part.content == b"0123456789" and part.headers["content-range"] == "bytes 10-19/100"
    assert client.get(signed(key, movie_rel, exp=1_000_000_000)).status_code == 403  # expired
    assert client.get(good.replace(str(FAR), str(FAR + 1))).status_code == 403  # tampered expiry
    assert client.get(good[:-1] + ("0" if good[-1] != "0" else "1")).status_code == 403  # tampered signature
    assert client.get(f"/media/file/{quote(movie_rel)}").status_code == 403  # unsigned
    assert client.get(signed(key, "movies/Nope/Nope.mkv")).status_code == 404
    assert client.get(signed(key, "movies/../../atrium.db")).status_code in (403, 404)


def test_guard_admits_signed_file_links_without_group(world):
    w = world
    movie_rel, _ = library_files(w["root"])
    proxy = TestClient(web.create_app(w["cfg"], allowed_hosts={"testserver"}, env=w["env"]))
    key = links.key(w["settings"])
    good = signed(key, movie_rel)
    assert proxy.get(good).status_code == 200
    assert proxy.get(good, headers={"Host": "evil.test"}).status_code == 400
    assert proxy.get(good.replace(str(FAR), str(FAR - 1))).status_code == 403
    assert proxy.get(f"/media/file/{quote(movie_rel)}").status_code == 403
    assert proxy.get("/media/downloads").status_code == 403
    assert proxy.get("/media/downloads", headers=GROUP).status_code == 200
    assert proxy.get(good, headers=GROUP).status_code == 200


def test_add_page_prefill_and_submit(world):
    w = world
    client = web_client(w["cfg"], env=w["env"])
    magnet = f"magnet:?xt=urn:btih:{HASH_A}&dn=Lanterns.S01E08"
    page = client.get("/media/add", params={"magnet": magnet}).text
    assert f'value="{magnet.replace("&", "&amp;")}"' in page and 'name="torrent"' in page and 'value="media" checked' in page
    done = client.post("/media/add", data={"magnet": magnet, "mode": "media"})
    assert done.status_code == 200 and "Added the magnet link" in done.text
    assert w["qb"].added[-1] == {"kind": "magnet", "magnet": magnet, "hash": HASH_A, "tags": ["media"]}
    plain = f"magnet:?xt=urn:btih:{HASH_B}"
    assert client.post("/media/add", data={"magnet": plain, "mode": "plain"}).status_code == 200
    assert w["qb"].added[-1]["tags"] == []
    data, hash_ = torrent_bytes("Dune.2021")
    b64 = base64.urlsafe_b64encode(data).decode().rstrip("=")
    assert client.post("/media/add", data={"torrent": b64, "name": "Dune.2021.torrent", "mode": "media"}).status_code == 200
    assert w["qb"].added[-1] == {"kind": "file", "name": "Dune.2021.torrent", "hash": hash_, "tags": ["media"]}
    assert "selected" in w["qb"].torrents[hash_]["tags"]  # metadata known at once, so selection applied
    assert client.post("/media/add", data={"magnet": "", "mode": "media"}).status_code == 422
    assert client.post("/media/add", data={"magnet": "http://x", "mode": "media"}).status_code == 422
    assert client.post("/media/add", data={"torrent": "AAAA", "mode": "media"}).status_code == 422
    w["qb"].fail = 500
    assert client.post("/media/add", data={"magnet": magnet, "mode": "media"}).status_code == 502


def test_downloads_page_lists_torrents_queue_and_files(world):
    w = world
    fake = w["qb"]
    completed_media(w)
    fake.add(HASH_B, "Dune.2021.mkv", tags=("media",), state="downloading", progress=0.4, size=2_000_000_000, files=[fake.file(0, "Dune.2021.mkv", size=2_000_000_000), fake.file(1, "x.nfo", priority=0)])
    fake.torrents[HASH_B]["dlspeed"], fake.torrents[HASH_B]["eta"] = 5_000_000, 400
    client = web_client(w["cfg"], env=w["env"])
    page = client.get("/media/downloads").text
    assert "Lanterns.S01E08.1080p.WEB.h264-ETHEL" in page and "Ready to file (1)" in page and "tv/Lanterns/Season 01/Lanterns - S01E08.mkv" in page
    assert "40%" in page and "4.8 MB/s" in page and "ETA 6m" in page and 'hx-get="/media/torrents"' in page
    assert client.get("/media", follow_redirects=False).headers["location"] == "/media/downloads"
    partial = client.get("/media/torrents").text
    assert "Dune.2021.mkv" in partial
    files = client.get(f"/media/torrents/{HASH_B}/files").text
    assert "Dune.2021.mkv" in files and "Skipped" in files and "Wanted" in files
    toggled = client.post(f"/media/torrents/{HASH_B}/files", data={"index": "1", "wanted": "1"}).text
    assert fake.torrents[HASH_B]["files"][1]["priority"] == 1 and "Selection changed" in toggled
    assert client.get(f"/media/torrents/{HASH_C}/files").status_code == 404
    fake.fail = 500
    down = client.get("/media/downloads")
    assert down.status_code == 200 and "qBittorrent: torrents/info: 500" in down.text


def test_downloads_page_without_qbittorrent_configured(world):
    w = world
    client = web_client(w["cfg"], env={k: v for k, v in w["env"].items() if k != "QBITTORRENT_URL"})
    page = client.get("/media/downloads")
    assert page.status_code == 200 and "QBITTORRENT_URL is not set" in page.text


def test_filing_from_the_web(world):
    w = world
    content = completed_media(w)
    client = web_client(w["cfg"], env=w["env"])
    preview = client.post("/media/file", data={"id": HASH_A, "action": "preview", "kind": "tv", "title": "Lanterns", "year": "2026", "s0": "1", "e0": "8"})
    assert preview.status_code == 200 and "tv/Lanterns (2026)/Season 01/Lanterns (2026) - S01E08.mkv" in preview.text
    assert not (w["root"] / "tv/Lanterns (2026)").exists()
    bad = client.post("/media/file", data={"id": HASH_A, "action": "file", "kind": "tv", "title": "Lanterns", "year": "20x6", "s0": "1", "e0": "8"})
    assert bad.status_code == 422 and "is not a year" in bad.text
    filed = client.post("/media/file", data={"id": HASH_A, "action": "file", "kind": "tv", "title": "Lanterns", "year": "2026", "s0": "1", "e0": "8"})
    assert filed.status_code == 200 and "Filed Lanterns.S01E08" in filed.text
    assert (w["root"] / "tv/Lanterns (2026)/Season 01/Lanterns (2026) - S01E08.mkv").stat().st_ino == (content / "Lanterns.S01E08.1080p.WEB.h264-ETHEL.mkv").stat().st_ino
    assert client.post("/media/file", data={"id": HASH_A, "action": "file"}).status_code == 422
    completed_media(w, HASH_B, name="Other.S01E01")
    ignored = client.post("/media/ignore", data={"id": HASH_B})
    assert ignored.status_code == 200 and "ignored" in w["qb"].torrents[HASH_B]["tags"]
    assert client.post("/media/ignore", data={"id": "nope"}).status_code == 422


def test_channels_page_search_preview_add_remove_poll(world):
    w = world
    w["tmdb"].shows[42] = {"name": "Lanterns", "first_air_date": "2026-01-01", "imdb_id": "tt1234567"}
    w["eztv"].release("1234567", HASH_A, "Lanterns S01E01 1080p WEB")
    w["eztv"].release("1234567", HASH_B, "lanterns s01e01 720p")
    client = web_client(w["cfg"], env=w["env"])
    page = client.get("/media/channels", params={"q": "lant"}).text
    assert "Lanterns" in page and "tmdb_id=42" in page
    picked = client.get("/media/channels", params={"tmdb_id": "42", "name": "Lanterns"}).text
    assert 'value="tt1234567"' in picked and 'value="42"' in picked
    preview = client.post("/media/channels/preview", data={"imdb": "tt1234567", "pattern": "^lanterns s01e01", "case_sensitive": ""}).text
    assert "2 of 2 current EZTV releases match" in preview
    strict = client.post("/media/channels/preview", data={"imdb": "tt1234567", "pattern": "^lanterns s01e01", "case_sensitive": "1"}).text
    assert "1 of 2 current EZTV releases match" in strict and 'data-match="no"' in strict
    assert "IMDb id first" in client.post("/media/channels/preview", data={"imdb": "", "pattern": "x"}).text
    assert "pattern" in client.post("/media/channels/preview", data={"imdb": "tt1234567", "pattern": "("}).text
    saved = client.post("/media/channels", data={"name": "Lanterns", "imdb": "tt1234567", "tmdb_id": "42", "pattern": "1080p", "case_sensitive": ""})
    assert saved.status_code == 200 and "Watching Lanterns" in saved.text
    assert client.post("/media/channels", data={"name": "Lanterns", "imdb": "nope", "pattern": "1080p"}).status_code == 422
    rows = channels.list_channels(w["conn"])
    assert len(rows) == 1 and rows[0]["tmdb_id"] == 42
    polled = client.post("/media/channels/poll")
    assert polled.status_code == 200 and "nothing new" in polled.text and channels.is_seen(w["conn"], HASH_A)
    removed = client.post(f"/media/channels/{rows[0]['id']}/delete")
    assert removed.status_code == 200 and "Removed Lanterns" in removed.text and channels.list_channels(w["conn"]) == []
    assert client.post("/media/channels/999/delete").status_code == 404


def test_channels_page_without_tmdb(world):
    w = world
    client = web_client(w["cfg"], env={k: v for k, v in w["env"].items() if k != "TMDB_API_KEY"})
    page = client.get("/media/channels", params={"q": "x"}).text
    assert "TMDB_API_KEY is not set" in page and 'name="imdb"' in page


def test_library_pages_posters_and_thumbnails(world, monkeypatch):
    w = world
    movie_rel, episode_rel = library_files(w["root"])
    w["tmdb"].movies.append({"title": "Dune", "release_date": "2021-10-22", "poster_path": "/dune.jpg"})
    w["tmdb"].shows[42] = {"name": "Lanterns", "first_air_date": "2026-01-01", "imdb_id": "tt1234567", "poster_path": "/lant.jpg", "seasons": {1: "/s1.jpg"}}
    ff = FakeFfmpeg()
    monkeypatch.setattr(artwork, "runner", ff)
    client = web_client(w["cfg"], env=w["env"])
    index = client.get("/media/library").text
    assert "Dune" in index and "Lanterns" in index and "/media/art/movie/Dune%20%282021%29" in index
    movie = client.get("/media/library/movie/Dune (2021)").text
    assert "Public link" in movie and "VPN link" in movie and "http://artemis.vpn:8084/Dune%20%282021%29/Dune%20%282021%29.mkv" in movie
    public = re.search(r'href="(https://atrium\.test/media/file/[^"]+)"', movie).group(1).replace("&amp;", "&")
    assert client.get(public.removeprefix("https://atrium.test")).status_code == 200
    show = client.get("/media/library/tv/Lanterns (2026)").text
    assert "Season 01" in show and "Episode 8" in show and "/media/art/season/Lanterns%20%282026%29/1" in show and "/media/thumb/" in show
    assert client.get("/media/library/tv/Nope").status_code == 404 and client.get("/media/library/movie/%2e%2e").status_code == 404

    poster = client.get("/media/art/movie/Dune (2021)")
    assert poster.status_code == 200 and poster.headers["content-type"] == "image/jpeg" and poster.content == JPEG
    assert client.get("/media/art/show/Lanterns (2026)").content == JPEG
    assert client.get("/media/art/season/Lanterns (2026)/1").content == JPEG
    assert w["tmdb"].images == 3
    assert client.get("/media/art/movie/Dune (2021)").content == JPEG and w["tmdb"].images == 3  # cached
    none = client.get("/media/art/movie/Unknown (1999)")
    assert none.status_code == 200 and none.headers["content-type"].startswith("image/svg+xml")
    assert client.get("/media/art/season/Lanterns (2026)/2").headers["content-type"].startswith("image/svg+xml")
    assert client.get("/media/art/bogus/x").status_code == 404

    thumb = client.get(f"/media/thumb/{quote(episode_rel)}")
    assert thumb.status_code == 200 and thumb.content == JPEG
    assert ff.calls[0][:5] == ["nice", "-n", "10", "ffprobe", "-v"] and ff.calls[1][3] == "ffmpeg" and "120.05" in ff.calls[1]
    assert client.get(f"/media/thumb/{quote(episode_rel)}").content == JPEG and len(ff.calls) == 2  # cached
    assert client.get("/media/thumb/movies/Dune%20%282021%29/missing.mkv").headers["content-type"].startswith("image/svg+xml")
    assert artwork.thumb_file(w["settings"].cache, episode_rel).is_file()


def test_thumbnail_pass_and_failures(world, monkeypatch):
    w = world
    _, episode_rel = library_files(w["root"])
    ff = FakeFfmpeg(fail=True)
    monkeypatch.setattr(artwork, "runner", ff)
    assert artwork.thumbnail(w["settings"].cache, w["root"], episode_rel) is None
    assert artwork.thumb_file(w["settings"].cache, episode_rel).with_suffix(".none").is_file()
    assert artwork.thumbnail(w["settings"].cache, w["root"], episode_rel) is None and len(ff.calls) == 2  # miss remembered
    (w["root"] / "tv/Lanterns (2026)/Season 01/Lanterns (2026) - S01E09.mkv").write_bytes(b"x")
    good = FakeFfmpeg()
    monkeypatch.setattr(artwork, "runner", good)
    assert artwork.thumbnail_pass(w["settings"].cache, w["root"]) == {"generated": 1, "remaining": 0}
    assert builtin_jobs.media_thumbnails(w["cfg"], w["conn"], env=w["env"]) == {"generated": 0, "remaining": 0}
    assert artwork.thumbnail_pass(w["settings"].cache, w["root"], limit=0) == {"generated": 0, "remaining": 1}  # the failed one still counts
    assert library.episode_files(w["root"]) == [episode_rel, "tv/Lanterns (2026)/Season 01/Lanterns (2026) - S01E09.mkv"]


def test_library_search(world):
    w = world
    library_files(w["root"])
    found = library.search(w["root"], "dune")
    assert [r["kind"] for r in found] == ["movie"] and found[0]["files"] == ["movies/Dune (2021)/Dune (2021).mkv"]
    episodes = library.search(w["root"], "lanterns s01e08")
    assert episodes[0]["kind"] == "episode" and episodes[0]["files"][0].endswith("S01E08.mkv")
    assert library.search(w["root"], "") == [] and library.search(w["root"], "nothing here") == []
    assert library.safe_rel(w["root"], "movies/../atrium.db") is None and library.safe_rel(w["root"], "/etc/passwd") is None


# ---------- navigation ----------


def test_nav_groups(world):
    page = web_client(world["cfg"], env=world["env"]).get("/chat").text
    for label in ("Assistant", "Mail", "Media", "/media/downloads", "/media/channels", "/media/library", "/media/add"):
        assert label in page
    assert "Organise" not in page


# ---------- agent tools ----------

def test_tool_contexts():
    reads = {"media_downloads", "media_library_search", "media_channels"}
    writes_ = {"media_add_magnet", "media_channel_add", "media_channel_remove"}
    for kind in ("chat", "cron", "code"):
        assert reads <= {t.name for t in registry.tools_for(kind)}, kind
    assert not reads & {t.name for t in registry.tools_for("nightly")}
    # Subagents only see what their allowlist names: the built-in web and mail agents never get media reads.
    assert reads <= {t.name for t in registry.subagent_pool()}
    for _, _, _, tools in subagents.BUILTINS:
        assert not reads & {t.name for t in registry.run_tools("sub", tools)}
    assert writes_ <= {t.name for t in registry.tools_for("chat")}
    for kind in ("cron", "nightly", "code", "sub"):
        assert not writes_ & {t.name for t in registry.tools_for(kind)}, kind
    by_name = {t.name: t for t in media_tools.TOOLS}
    assert all(by_name[n].effect == "write" for n in writes_) and all(by_name[n].effect == "read" for n in reads)


def test_read_tools(world):
    w = world
    completed_media(w)
    library_files(w["root"])
    channels.add_channel(w["conn"], "Lanterns", "tt1234567", "1080p", now=lambda: NOW)
    ctx = make_ctx(w["conn"], w["cfg"], env=w["env"], now=lambda: NOW)
    out = media_tools.media_downloads(ctx, {})
    assert out["torrents"][0]["kind"] == "media" and out["torrents"][0]["state"] == "done" and out["waiting_to_file"][0]["id"] == HASH_A
    found = media_tools.media_library_search(ctx, {"query": "dune"})
    assert found["results"][0]["vpn_urls"] == ["http://artemis.vpn:8084/Dune%20%282021%29/Dune%20%282021%29.mkv"]
    assert media_tools.media_channels(ctx, {})["channels"][0]["name"] == "Lanterns"
    with pytest.raises(ToolError):
        media_tools.media_library_search(ctx, {})
    bare = make_ctx(w["conn"], w["cfg"], env={}, now=lambda: NOW)
    with pytest.raises(NotConfigured):
        media_tools.media_downloads(bare, {})
    with pytest.raises(NotConfigured):
        media_tools.media_library_search(bare, {"query": "x"})
    w["qb"].fail = 500
    with pytest.raises(ToolError):
        media_tools.media_downloads(ctx, {})


def test_add_magnet_tool_and_undo(world):
    w = world
    ctx = make_ctx(w["conn"], w["cfg"], env=w["env"], now=lambda: NOW)
    magnet = f"magnet:?xt=urn:btih:{HASH_A}&dn=x"
    out = media_tools.media_add_magnet(ctx, {"magnet": magnet})
    assert out["hash"] == HASH_A and out["kind"] == "media" and w["qb"].added[-1]["tags"] == ["media"]
    row = writes.get(w["conn"], out["write_id"])
    assert row["tool"] == "media_add_magnet" and json.loads(row["undo_json"]) == {"op": "media_torrent_delete", "hash": HASH_A}
    writes.undo(w["conn"], w["cfg"], w["env"], out["write_id"])
    assert w["qb"].deleted == [(HASH_A, True)] and HASH_A not in w["qb"].torrents
    with pytest.raises(writes.UndoError, match="already undone"):
        writes.undo(w["conn"], w["cfg"], w["env"], out["write_id"])

    plain = media_tools.media_add_magnet(ctx, {"magnet": f"magnet:?xt=urn:btih:{HASH_B}", "media": False})
    assert plain["kind"] == "plain" and w["qb"].added[-1]["tags"] == []
    w["qb"].torrents[HASH_B]["tags"].append("filed")
    with pytest.raises(writes.UndoError, match="already filed"):
        writes.undo(w["conn"], w["cfg"], w["env"], plain["write_id"])
    assert HASH_B in w["qb"].torrents
    del w["qb"].torrents[HASH_B]
    with pytest.raises(writes.UndoError, match="no longer"):
        writes.undo(w["conn"], w["cfg"], w["env"], plain["write_id"])

    for bad in ({"magnet": "http://x"}, {"magnet": "magnet:?dn=nohash"}, {"magnet": magnet, "media": "yes"}):
        with pytest.raises(ToolError):
            media_tools.media_add_magnet(ctx, bad)
    with pytest.raises(NotConfigured):
        media_tools.media_add_magnet(make_ctx(w["conn"], w["cfg"], env={}, now=lambda: NOW), {"magnet": magnet})


def test_channel_tools_and_undo(world):
    w = world
    w["tmdb"].shows[42] = {"name": "Lanterns", "first_air_date": "2026-01-01", "imdb_id": "tt1234567"}
    w["tmdb"].shows[43] = {"name": "No IMDb", "first_air_date": "2026-01-01"}
    ctx = make_ctx(w["conn"], w["cfg"], env=w["env"], now=lambda: NOW)
    out = media_tools.media_channel_add(ctx, {"tmdb_id": 42, "regex": "1080p"})
    assert (out["name"], out["imdb"], out["tmdb_id"], out["case_sensitive"]) == ("Lanterns", "1234567", 42, False)
    writes.undo(w["conn"], w["cfg"], w["env"], out["write_id"])
    assert channels.list_channels(w["conn"]) == []

    by_imdb = media_tools.media_channel_add(ctx, {"imdb_id": "tt1234567", "regex": "720p", "case_sensitive": True})
    assert by_imdb["name"] == "Lanterns" and by_imdb["case_sensitive"] is True
    named = media_tools.media_channel_add(ctx, {"imdb_id": "7654321", "regex": "x", "name": "Custom"})
    assert named["name"] == "Custom"
    removed = media_tools.media_channel_remove(ctx, {"id": by_imdb["id"]})
    assert removed["removed"] == by_imdb["id"] and [c["id"] for c in channels.list_channels(w["conn"])] == [named["id"]]
    writes.undo(w["conn"], w["cfg"], w["env"], removed["write_id"])
    restored = channels.get_channel(w["conn"], by_imdb["id"])
    assert restored["pattern"] == "720p" and restored["case_sensitive"] is True

    for bad in ({"regex": "x"}, {"imdb_id": "bad", "regex": "x"}, {"tmdb_id": 43, "regex": "x"}, {"tmdb_id": "42", "regex": "x"}, {"imdb_id": "tt1234567", "regex": "("}):
        with pytest.raises(ToolError):
            media_tools.media_channel_add(ctx, bad)
    for bad in ({"id": "x"}, {"id": 999}):
        with pytest.raises(ToolError):
            media_tools.media_channel_remove(ctx, bad)
    no_tmdb = make_ctx(w["conn"], w["cfg"], env={k: v for k, v in w["env"].items() if k != "TMDB_API_KEY"}, now=lambda: NOW)
    with pytest.raises(NotConfigured):
        media_tools.media_channel_add(no_tmdb, {"tmdb_id": 42, "regex": "x"})
    assert media_tools.media_channel_add(no_tmdb, {"imdb_id": "tt1111111", "regex": "x"})["name"] == "tt1111111"
