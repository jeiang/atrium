"""In-memory stand-ins for qBittorrent, EZTV, TMDB, ntfy and ffmpeg; nothing here touches the network."""

import email.parser
import email.policy
import json
import subprocess
from urllib.parse import parse_qs

import httpx

from atrium.media import qbit

JPEG = b"\xff\xd8\xff\xdb" + b"\x00" * 16 + b"\xff\xd9"


def _form(request):
    content_type = request.headers.get("content-type", "")
    if content_type.startswith("multipart/form-data"):
        raw = f"Content-Type: {content_type}\r\n\r\n".encode() + request.content
        message = email.parser.BytesParser(policy=email.policy.default).parsebytes(raw)
        fields, files = {}, {}
        for part in message.iter_parts():
            name = part.get_param("name", header="content-disposition")
            filename = part.get_filename()
            if filename:
                files[name] = (filename, part.get_payload(decode=True))
            else:
                fields[name] = part.get_payload(decode=True).decode()
        return fields, files
    return {k: v[0] for k, v in parse_qs(request.content.decode()).items()}, {}


class FakeQbit:
    """qBittorrent Web API v2 subset: info, files, filePrio, add, addTags, delete, start."""

    def __init__(self):
        self.torrents = {}
        self.added = []
        self.deleted = []
        self.started = []
        self.fail = None
        self.transport = httpx.MockTransport(self.handle)

    def add(self, hash_, name, *, tags=(), state=qbit.STOPPED_UP, content_path="", files=(), progress=None, size=0):
        self.torrents[hash_] = {
            "hash": hash_, "name": name, "tags": list(tags), "state": state, "content_path": content_path or name,
            "progress": (1.0 if state.endswith("UP") else 0.5) if progress is None else progress, "size": size or sum(f.get("size", 0) for f in files),
            "dlspeed": 0, "upspeed": 0, "eta": 8640000, "files": [dict(f) for f in files],
        }
        return self.torrents[hash_]

    def file(self, index, name, size=1000, priority=qbit.PRIORITY_NORMAL):
        return {"index": index, "name": name, "size": size, "progress": 0.0, "priority": priority}

    def _info(self, t):
        return {k: v for k, v in t.items() if k != "files"} | {"tags": ", ".join(t["tags"])}

    def handle(self, request):
        if self.fail:
            return httpx.Response(self.fail, text="down")
        path = request.url.path.removeprefix("/api/v2/")
        params = dict(request.url.params)
        if path == "torrents/info":
            rows = list(self.torrents.values())
            if params.get("filter") == "completed":
                rows = [t for t in rows if t["progress"] >= 1.0]
            if params.get("tag"):
                rows = [t for t in rows if params["tag"] in t["tags"]]
            if params.get("hashes"):
                wanted = params["hashes"].split("|")
                rows = [t for t in rows if t["hash"] in wanted]
            return httpx.Response(200, json=[self._info(t) for t in rows])
        if path == "torrents/files":
            t = self.torrents.get(params.get("hash"))
            return httpx.Response(404) if t is None else httpx.Response(200, json=t["files"])
        fields, files = _form(request)
        if path == "torrents/add":
            tags = [x for x in (fields.get("tags") or "").split(",") if x]
            if "torrents" in files:
                filename, data = files["torrents"]
                hash_ = qbit.torrent_hash(data)
                self.added.append({"kind": "file", "name": filename, "hash": hash_, "tags": tags})
                self.add(hash_, filename, tags=tags, state="downloading", files=[self.file(0, filename.removesuffix(".torrent") + ".mkv")])
            else:
                magnet = fields.get("urls", "")
                hash_ = qbit.magnet_hash(magnet) or "nohash"
                self.added.append({"kind": "magnet", "magnet": magnet, "hash": hash_, "tags": tags})
                if hash_ not in self.torrents:
                    self.add(hash_, f"magnet {hash_[:8]}", tags=tags, state="metaDL")
            return httpx.Response(200, json={"added_torrent_ids": [hash_], "failure_count": 0, "pending_count": 0, "success_count": 1})
        if path == "torrents/addTags":
            for h in fields["hashes"].split("|"):
                for tag in fields["tags"].split(","):
                    if h in self.torrents and tag not in self.torrents[h]["tags"]:
                        self.torrents[h]["tags"].append(tag)
            return httpx.Response(200)
        if path == "torrents/filePrio":
            t = self.torrents[fields["hash"]]
            for i in fields["id"].split("|"):
                t["files"][int(i)]["priority"] = int(fields["priority"])
            return httpx.Response(200)
        if path == "torrents/delete":
            for h in fields["hashes"].split("|"):
                self.deleted.append((h, fields.get("deleteFiles") == "true"))
                self.torrents.pop(h, None)
            return httpx.Response(200)
        if path == "torrents/start":
            for h in fields["hashes"].split("|"):
                self.started.append(h)
                if h in self.torrents:
                    self.torrents[h]["state"] = "downloading"
            return httpx.Response(200)
        return httpx.Response(404)


class FakeEztv:
    def __init__(self):
        self.releases = {}  # imdb digits -> list of torrents
        self.calls = []
        self.fail = None
        self.transport = httpx.MockTransport(self.handle)

    def release(self, imdb, hash_, title, size="1500000000"):
        self.releases.setdefault(imdb, []).append({"hash": hash_, "title": title, "magnet_url": f"magnet:?xt=urn:btih:{hash_}&dn=x", "size_bytes": size, "season": "1", "episode": "1"})

    def handle(self, request):
        self.calls.append(dict(request.url.params))
        if self.fail:
            return httpx.Response(self.fail)
        return httpx.Response(200, json={"torrents": self.releases.get(request.url.params.get("imdb_id"), [])})


class FakeTmdb:
    def __init__(self):
        self.shows = {}  # tmdb id -> {"name", "first_air_date", "imdb_id", "poster_path", "seasons": {n: poster_path}}
        self.movies = []  # {"title", "release_date", "poster_path"}
        self.images = 0
        self.transport = httpx.MockTransport(self.handle)

    def handle(self, request):
        if request.url.host == "image.tmdb.org":
            self.images += 1
            return httpx.Response(200, content=JPEG, headers={"content-type": "image/jpeg"})
        path = request.url.path.removeprefix("/3/")
        params = dict(request.url.params)
        if params.get("api_key") != "k":
            return httpx.Response(401)
        if path == "search/tv":
            q = params["query"].casefold()
            found = [{"id": i, "name": s["name"], "first_air_date": s["first_air_date"], "poster_path": s.get("poster_path")} for i, s in self.shows.items() if q in s["name"].casefold()]
            return httpx.Response(200, json={"results": found})
        if path == "search/movie":
            q = params["query"].casefold()
            found = [m for m in self.movies if q in m["title"].casefold() and (not params.get("year") or m["release_date"].startswith(params["year"]))]
            return httpx.Response(200, json={"results": found})
        if path.startswith("find/tt"):
            digits = path.removeprefix("find/tt")
            found = [{"id": i, "name": s["name"], "first_air_date": s["first_air_date"]} for i, s in self.shows.items() if s.get("imdb_id") == f"tt{digits}"]
            return httpx.Response(200, json={"tv_results": found})
        if path.startswith("tv/"):
            parts = path.split("/")
            show = self.shows.get(int(parts[1]))
            if show is None:
                return httpx.Response(404)
            if len(parts) == 2:
                return httpx.Response(200, json={"id": int(parts[1]), "name": show["name"], "poster_path": show.get("poster_path")})
            if parts[2] == "external_ids":
                return httpx.Response(200, json={"imdb_id": show.get("imdb_id")})
            if parts[2] == "season":
                poster = show.get("seasons", {}).get(int(parts[3]))
                return httpx.Response(200, json={"poster_path": poster}) if poster else httpx.Response(404)
        return httpx.Response(404)


class FakeNtfy:
    def __init__(self):
        self.sent = []
        self.fail = None
        self.transport = httpx.MockTransport(self.handle)

    def handle(self, request):
        if self.fail:
            return httpx.Response(self.fail)
        self.sent.append(json.loads(request.content))
        return httpx.Response(200)


class FakeFfmpeg:
    """Replaces subprocess.run for ffprobe/ffmpeg: writes a small JPEG where ffmpeg would."""

    def __init__(self, duration="1200.5", fail=False):
        self.duration = duration
        self.fail = fail
        self.calls = []

    def __call__(self, args, **kwargs):
        self.calls.append(list(args))
        tool = args[args.index("-n") + 2] if "-n" in args else args[0]
        if tool == "ffprobe":
            return subprocess.CompletedProcess(args, 0, stdout=self.duration + "\n", stderr="")
        if self.fail:
            return subprocess.CompletedProcess(args, 1, stdout="", stderr="boom")
        with open(args[-1], "wb") as handle:
            handle.write(JPEG)
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")
