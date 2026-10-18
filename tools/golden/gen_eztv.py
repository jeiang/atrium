"""EZTV request goldens: the media.eztv.fetch request, answered by tests/fake_media.py. The channel poll flow that
calls it is in testdata/golden/qbittorrent/channel_poll.json. Output: testdata/golden/eztv/ (or argv[1])."""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from wire import httpx_fake, reply, write_slice  # noqa: E402

from atrium.media import eztv  # noqa: E402
from fake_media import FakeEztv  # noqa: E402

SLICE = "eztv"


def fetch(wire, tmp):
    fake = FakeEztv()
    fake.release("1234567", "A" * 40, "Lanterns S01E07 1080p WEB h264")
    fake.release("1234567", "b" * 40, "Lanterns S01E07 720p", size="1.2e9")
    wire.serve(eztv.API, httpx_fake(fake.handle))
    found = eztv.fetch(eztv.imdb_digits("tt1234567"))
    assert [t["hash"] for t in found] == ["a" * 40, "b" * 40]
    eztv.fetch("7654321", limit=20)


def errors(wire, tmp):
    answers = iter([reply(503, "busy"), reply(200, "<html>not json</html>")])
    wire.serve(eztv.API, lambda *a: next(answers))
    for _ in range(2):
        try:
            eztv.fetch("1234567")
        except eztv.EztvError:
            continue
        raise AssertionError("EZTV failure must raise")


SCENARIOS = [
    ("fetch", "get-torrents with imdb_id digits and limit (default 100, then 20); releases without hash or magnet are dropped, hashes lower-cased.", ["tests/test_media.py::test_poll_primes_then_adds_tagged_and_notifies", "tests/test_media.py::test_channels_page_search_preview_add_remove_poll"], fetch),
    ("errors", "A non-200 status and a non-JSON body both raise EztvError.", ["tests/test_media.py::test_poll_primes_then_adds_tagged_and_notifies"], errors),
]


if __name__ == "__main__":
    write_slice(SLICE, "EZTV", __file__, SCENARIOS, "none: EZTV is unauthenticated")
