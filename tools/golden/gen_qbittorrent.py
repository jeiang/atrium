"""qBittorrent Web API v2 request goldens: every media.qbit.Qbit request and the channel poll flow, answered by
tests/fake_media.py. Output: testdata/golden/qbittorrent/ (or argv[1])."""

import hashlib
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from wire import httpx_fake, reply, write_slice  # noqa: E402

from atrium import db  # noqa: E402
from atrium.media import channels, qbit  # noqa: E402
from atrium.media import settings as media_settings  # noqa: E402
from fake_media import FakeEztv, FakeNtfy, FakeQbit  # noqa: E402
from helpers import make_config  # noqa: E402

SLICE = "qbittorrent"
BASE = "http://qbit.test"
NTFY = "https://ntfy.test"
NOW = 1_800_000_000.0
HASH_A, HASH_B, HASH_C = "a" * 40, "b" * 40, "c" * 40
MAGNET = f"magnet:?xt=urn:btih:{HASH_C}&dn=Lanterns.S01E08"


def torrent_bytes(name="Show.S01E01.1080p"):
    info = b"d4:name" + f"{len(name)}:{name}".encode() + b"12:piece lengthi16384e6:pieces20:" + b"p" * 20 + b"e"
    return b"d8:announce9:http://x/4:info" + info + b"e", hashlib.sha1(info).hexdigest()


def served(wire):
    fake = FakeQbit()
    wire.serve(BASE, httpx_fake(fake.handle))
    return fake


def client_requests(wire, tmp):
    fake = served(wire)
    fake.add(HASH_A, "Lanterns.S01E07", tags=("media",), state="downloading", progress=0.4, files=[fake.file(0, "e.mkv"), fake.file(1, "e.nfo")])
    fake.add(HASH_B, "Album", tags=(), state=qbit.STOPPED_UP)
    qb = qbit.Qbit(BASE + "/")
    qb.torrents()
    qb.completed()
    qb.torrents(tag=qbit.TAG_MEDIA)
    qb.torrent(HASH_A)
    qb.files(HASH_A)
    qb.add_magnet(MAGNET, tags=qbit.TAG_MEDIA)
    qb.add_magnet(f"magnet:?xt=urn:btih:{'d' * 40}")
    data, _ = torrent_bytes()
    qb.add_file(data, "Show.S01E01.1080p.torrent", tags=qbit.TAG_MEDIA)
    qb.add_file(data, "plain.torrent")
    qb.add_tags(HASH_B, qbit.TAG_FILED)
    qb.set_priority(HASH_A, [1], qbit.PRIORITY_SKIP)
    qb.set_priority(HASH_A, [0, 1], qbit.PRIORITY_NORMAL)
    qb.start(HASH_A)
    qb.delete(HASH_B, files=True)
    qb.delete(HASH_A, files=False)


def add_answers(wire, tmp):
    answers = iter([reply(200, "Ok."), reply(200, {"added_torrent_ids": [], "failure_count": 1, "pending_count": 0, "success_count": 0}), reply(200, "Fails.")])
    wire.serve(BASE, lambda *a: next(answers))
    qb = qbit.Qbit(BASE)
    qb.add_magnet(MAGNET)
    for _ in range(2):
        try:
            qb.add_magnet(MAGNET)
        except qbit.QbitError:
            pass
        else:
            raise AssertionError("a rejected add must raise")


def channel_poll(wire, tmp):
    fake = served(wire)
    ez, nt = FakeEztv(), FakeNtfy()
    wire.serve("https://eztvx.to", httpx_fake(ez.handle))
    wire.serve(NTFY, httpx_fake(nt.handle))
    ez.release("1234567", HASH_A, "Lanterns S01E07 1080p WEB h264")
    ez.release("1234567", HASH_B, "Lanterns S01E07 720p")
    cfg = make_config(tmp)
    env = {"QBITTORRENT_URL": BASE, "ATRIUM_PUBLIC_URL": "https://atrium.test", "NTFY_URL": NTFY, "NTFY_TOKEN": "golden-ntfy-token", "NTFY_MEDIA_TOPIC": "media-releases"}
    settings = media_settings.load(cfg, env)
    conn = db.connect(cfg.db_path)
    channels.add_channel(conn, "Lanterns", "tt1234567", r"^Lanterns S\d+E\d+ 1080p", now=lambda: NOW)
    qb = qbit.Qbit(settings.need_qbit())
    assert channels.poll(conn, env, qb, settings, now=lambda: NOW)["added"] == []
    ez.release("1234567", HASH_C, "Lanterns S01E08 1080p WEB h264", size="2500000000")
    fake.add(HASH_C, "Lanterns S01E08", tags=("media",), state="stoppedDL", progress=0.0, files=[fake.file(0, "Lanterns.S01E08.mkv", priority=0), fake.file(1, "Lanterns.S01E08.nfo"), fake.file(2, "Lanterns.S01E08.en.srt", priority=0)])
    out = channels.poll(conn, env, qb, settings, now=lambda: NOW + 60)
    assert [a["hash"] for a in out["added"]] == [HASH_C] and out["errors"] == []
    conn.close()


SCENARIOS = [
    ("client_requests", "Every Qbit request: torrents/info (no filter, filter=completed, tag=media, hashes=), torrents/files, torrents/add (magnet with and without tags; .torrent upload as multipart with and without tags), addTags, filePrio (skip and normal), start, delete (deleteFiles true and false).", ["tests/test_media.py::test_select_files_keeps_video_and_subtitles_and_starts", "tests/test_media.py::test_add_page_prefill_and_submit", "tests/test_media.py::test_add_magnet_tool_and_undo", "src/atrium/media/qbit.py::Qbit (every method)"], client_requests),
    ("add_answers", "torrents/add accepted with the legacy \"Ok.\" body, then refused by a JSON tally with failure_count and by \"Fails.\" (both raise QbitError).", ["tests/test_media.py::test_qbittorrent_add_replies"], add_answers),
    ("channel_poll", "channels.poll twice: the first pass primes (EZTV only), the second adds the new matching release tagged media, notifies ntfy with topic and click, then selects files (info tag=media, files, filePrio, addTags selected, start). Holds the EZTV and ntfy requests of the flow too.", ["tests/test_media.py::test_poll_primes_then_adds_tagged_and_notifies", "tests/test_media.py::test_poll_selects_files_of_added_torrents"], channel_poll),
]

NOTES = [
    "No login request: the Python client never calls auth/login; the host's loopback whitelist on the qBittorrent socket proxy admits it (src/atrium/media/qbit.py). The Go port keeps that.",
    f"The base URL is `{BASE}` (QBITTORRENT_URL); every path sits under `/api/v2/`.",
]


if __name__ == "__main__":
    write_slice(SLICE, "qBittorrent", __file__, SCENARIOS, "none: qBittorrent takes no credentials; ntfy in channel_poll uses `Authorization: Bearer golden-ntfy-token`", NOTES)
