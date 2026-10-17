"""qBittorrent Web API v2 client. The loopback whitelist on the host socket proxy makes login unnecessary."""

import base64
import hashlib
import re
from urllib.parse import parse_qs, urlsplit

import httpx

TAG_MEDIA = "media"  # added through atrium or a channel: filed into the library
TAG_FILED = "filed"
TAG_IGNORED = "ignored"
TAG_SELECTED = "selected"  # video/subtitle selection applied once metadata arrived

PRIORITY_SKIP = 0
PRIORITY_NORMAL = 1
TIMEOUT_SECONDS = 15
OK = "Ok."

ACTIVE_STATES = frozenset({
    "downloading", "metaDL", "forcedMetaDL", "stalledDL", "checkingDL", "forcedDL", "queuedDL", "allocating",
    "moving", "checkingResumeData", "uploading", "forcedUP", "checkingUP",
})
STOPPED_UP = "stoppedUP"
STOPPED_DL = "stoppedDL"
STATE_LABELS = {
    "downloading": "downloading", "metaDL": "fetching metadata", "forcedMetaDL": "fetching metadata",
    "stalledDL": "stalled", "checkingDL": "checking", "forcedDL": "downloading", "queuedDL": "queued",
    "allocating": "allocating", "moving": "moving", "checkingResumeData": "checking", "uploading": "seeding",
    "forcedUP": "seeding", "stalledUP": "seeding", "queuedUP": "queued", "checkingUP": "checking",
    STOPPED_UP: "done", STOPPED_DL: "stopped", "pausedUP": "done", "pausedDL": "stopped", "error": "error",
    "missingFiles": "missing files", "unknown": "unknown",
}

BTIH = re.compile(r"^urn:btih:([0-9a-fA-F]{40}|[A-Za-z2-7]{32})$")

transport = None  # tests install an httpx.MockTransport


class QbitError(Exception):
    pass


def has_tag(torrent, tag):
    return tag in (t.strip() for t in (torrent.get("tags") or "").split(","))


def magnet_hash(magnet):
    """Lower-case hex v1 info hash from a magnet link, or None when it has none."""
    if not magnet.startswith("magnet:?"):
        return None
    for xt in parse_qs(urlsplit(magnet).query).get("xt", []):
        m = BTIH.match(xt)
        if not m:
            continue
        value = m.group(1)
        if len(value) == 40:
            return value.lower()
        return base64.b32decode(value.upper()).hex()
    return None


def _bdecode(data, i):
    """Return (value, end) for the bencoded value at data[i:]; dict values keep their byte span."""
    c = data[i : i + 1]
    if c == b"i":
        end = data.index(b"e", i)
        return int(data[i + 1 : end]), end + 1
    if c == b"l":
        out, i = [], i + 1
        while data[i : i + 1] != b"e":
            value, i = _bdecode(data, i)
            out.append(value)
        return out, i + 1
    if c == b"d":
        out, i = {}, i + 1
        while data[i : i + 1] != b"e":
            key, i = _bdecode(data, i)
            start = i
            value, i = _bdecode(data, i)
            out[key] = (value, start, i)
        return out, i + 1
    if c.isdigit():
        colon = data.index(b":", i)
        length = int(data[i:colon])
        return data[colon + 1 : colon + 1 + length], colon + 1 + length
    raise ValueError("not bencoded")


def torrent_hash(data):
    """Lower-case hex v1 info hash of a .torrent file."""
    try:
        top, _ = _bdecode(data, 0)
        _, start, end = top[b"info"]
    except (ValueError, KeyError, IndexError, TypeError) as e:
        raise QbitError("not a torrent file") from e
    return hashlib.sha1(data[start:end]).hexdigest()


class Qbit:
    def __init__(self, base):
        self.base = base.rstrip("/")

    def _client(self):
        return httpx.Client(base_url=f"{self.base}/api/v2/", timeout=TIMEOUT_SECONDS, transport=transport)

    def _request(self, method, path, **kwargs):
        try:
            with self._client() as client:
                response = client.request(method, path, **kwargs)
        except httpx.HTTPError as e:
            raise QbitError(f"qBittorrent unreachable: {type(e).__name__}") from e
        if response.status_code != 200:
            raise QbitError(f"qBittorrent: {path}: {response.status_code}")
        return response

    def _get_json(self, path, params=None):
        try:
            return self._request("GET", path, params=params).json()
        except ValueError as e:
            raise QbitError(f"qBittorrent: {path}: not JSON") from e

    def _post(self, path, data=None, files=None):
        response = self._request("POST", path, data=data, files=files)
        if path == "torrents/add" and response.text.strip() != OK:
            raise QbitError(f"qBittorrent did not accept the torrent: {response.text.strip() or 'no reply'}")
        return response

    def torrents(self, **params):
        """torrents/info; params: filter=, tag=, hashes= (pipe-separated)."""
        return self._get_json("torrents/info", {k: v for k, v in params.items() if v})

    def completed(self):
        return self.torrents(filter="completed")

    def torrent(self, hash_):
        found = self.torrents(hashes=hash_)
        return found[0] if found else None

    def files(self, hash_):
        return self._get_json("torrents/files", {"hash": hash_})

    def add_magnet(self, magnet, tags=None):
        self._post("torrents/add", data={"urls": magnet, **({"tags": tags} if tags else {})})

    def add_file(self, data, name, tags=None):
        self._post("torrents/add", data={"tags": tags} if tags else {}, files={"torrents": (name, data, "application/x-bittorrent")})

    def add_tags(self, hash_, tags):
        self._post("torrents/addTags", data={"hashes": hash_, "tags": tags})

    def set_priority(self, hash_, ids, priority):
        if ids:
            self._post("torrents/filePrio", data={"hash": hash_, "id": "|".join(str(i) for i in ids), "priority": str(priority)})

    def start(self, hash_):
        self._post("torrents/start", data={"hashes": hash_})

    def delete(self, hash_, files=True):
        self._post("torrents/delete", data={"hashes": hash_, "deleteFiles": "true" if files else "false"})
