import base64
import binascii
import functools
import time
from typing import Annotated

from fastapi import Form, HTTPException, Request
from fastapi.responses import FileResponse, RedirectResponse, Response

from ..media import artwork, channels, eztv, library, links, organizer, qbit, tmdb
from ..media import settings as media_settings
from ..media.settings import DOWNLOADS_PAGE

MEDIA, PLAIN = "media", "plain"
MODES = (MEDIA, PLAIN)
POLL_SECONDS = 5
INFINITE_ETA = 8640000
TORRENT_B64_CAP = 1 << 20
PLACEHOLDER = b'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 2 3"><rect width="2" height="3" fill="#80808033"/></svg>'
IMAGE_CACHE = "private, max-age=86400"
PLACEHOLDER_CACHE = "private, max-age=600"
VIDEO_TYPES = {
    ".mkv": "video/x-matroska", ".mp4": "video/mp4", ".m4v": "video/x-m4v", ".avi": "video/x-msvideo",
    ".mov": "video/quicktime", ".ts": "video/mp2t", ".wmv": "video/x-ms-wmv",
}


def human_size(n):
    n = float(n or 0)
    for unit in ("B", "KB", "MB", "GB"):
        if n < 1024 or unit == "GB":
            return f"{n:.0f} {unit}" if unit == "B" else f"{n:.1f} {unit}"
        n /= 1024
    return f"{n:.1f} GB"


def human_eta(seconds):
    if not seconds or seconds < 0 or seconds >= INFINITE_ETA:
        return ""
    seconds = int(seconds)
    if seconds < 60:
        return f"{seconds}s"
    if seconds < 3600:
        return f"{seconds // 60}m"
    return f"{seconds // 3600}h {seconds % 3600 // 60:02d}m"


def torrent_view(t):
    state = t.get("state") or "unknown"
    return {
        "hash": t["hash"],
        "name": t["name"],
        "state": state,
        "label": qbit.STATE_LABELS.get(state, state),
        "active": state in qbit.ACTIVE_STATES,
        "percent": round(float(t.get("progress") or 0) * 100),
        "size": human_size(t.get("size")),
        "down": human_size(t.get("dlspeed")) + "/s" if t.get("dlspeed") else "",
        "up": human_size(t.get("upspeed")) + "/s" if t.get("upspeed") else "",
        "eta": human_eta(t.get("eta")),
        "kind": MEDIA if qbit.has_tag(t, qbit.TAG_MEDIA) else PLAIN,
        "filed": qbit.has_tag(t, qbit.TAG_FILED),
        "ignored": qbit.has_tag(t, qbit.TAG_IGNORED),
    }


def file_view(f):
    return {**f, "size_text": human_size(f.get("size")), "percent": round(float(f.get("progress") or 0) * 100), "wanted": f.get("priority") != qbit.PRIORITY_SKIP}


def decode_torrent(b64):
    b64 = "".join((b64 or "").split())
    if not b64:
        return None
    if len(b64) > TORRENT_B64_CAP:
        raise ValueError("the torrent file is too large")
    try:
        return base64.urlsafe_b64decode(b64 + "=" * (-len(b64) % 4))
    except (binascii.Error, ValueError) as e:
        raise ValueError("the torrent data is not base64") from e


def key_loader(settings):
    return functools.cache(lambda: links.key(settings))


def register(app, deps):
    cfg, render, connection, toast, env = deps.cfg, deps.render, deps.connection, deps.toast, deps.env
    settings = media_settings.load(cfg, env)
    get_key = key_loader(settings)
    tmdb_client = tmdb.from_settings(settings)

    def qb():
        return qbit.Qbit(settings.need_qbit())

    def root():
        return str(settings.need_root())

    def not_found(exc=None):
        raise HTTPException(404, "not found") from exc

    # ---------- downloads ----------

    def torrents():
        """Torrent views plus the error text; applies pending file selections first so a fresh add settles quickly."""
        try:
            client = qb()
            organizer.select_files(client)
            return [torrent_view(t) for t in client.torrents()], None
        except (media_settings.NotConfigured, qbit.QbitError) as e:
            return [], str(e)

    def queue(edited=None):
        try:
            found, error = organizer.items(qb(), root())
        except media_settings.NotConfigured as e:
            return [], str(e)
        views = []
        for it in found:
            if edited is not None and edited.id == it.id:
                views.append(edited)
            else:
                views.append(organizer.planned(organizer.defaults(it, root()), root()))
        return views, error

    def downloads(notice=None, status_code=200, edited=None):
        rows, error = torrents()
        waiting, queue_error = queue(edited)
        return render(
            "media_downloads.html", torrents=rows, error=error, active=any(t["active"] for t in rows), poll=POLL_SECONDS,
            queue=waiting, queue_error=queue_error, kinds=organizer.KINDS, toast=notice, status_code=status_code,
        )

    @app.get("/media")
    def media_home():
        return RedirectResponse(DOWNLOADS_PAGE, status_code=303)

    @app.get(DOWNLOADS_PAGE)
    def downloads_page():
        return downloads()

    @app.get("/media/torrents")
    def torrents_partial():
        rows, error = torrents()
        return render("_media_torrents.html", torrents=rows, error=error, active=any(t["active"] for t in rows), poll=POLL_SECONDS)

    def files_partial(hash_, notice=None):
        try:
            client = qb()
            found = client.torrent(hash_)
            if found is None:
                raise HTTPException(404, "unknown torrent")
            files = [file_view(f) for f in client.files(hash_)]
        except (media_settings.NotConfigured, qbit.QbitError) as e:
            return render("_media_files.html", hash=hash_, torrent_name="", files=[], error=str(e), status_code=502)
        return render("_media_files.html", hash=hash_, torrent_name=found["name"], files=files, error=None, toast=notice)

    @app.get("/media/torrents/{hash_}/files")
    def torrent_files(hash_: str):
        return files_partial(hash_)

    @app.post("/media/torrents/{hash_}/files")
    def torrent_file_toggle(hash_: str, index: Annotated[int, Form()], wanted: Annotated[int, Form()] = 1):
        try:
            qb().set_priority(hash_, [index], qbit.PRIORITY_NORMAL if wanted else qbit.PRIORITY_SKIP)
        except (media_settings.NotConfigured, qbit.QbitError) as e:
            return render("_media_files.html", hash=hash_, torrent_name="", files=[], error=str(e), status_code=502)
        return files_partial(hash_, toast("success", "Selection changed"))

    # ---------- add ----------

    def add_page(magnet="", name="", mode=MEDIA, error=None, status_code=200):
        return render("media_add.html", magnet=magnet, torrent_name=name, mode=mode, error=error, modes=MODES, status_code=status_code)

    @app.get("/media/add")
    def add_form(magnet: str = ""):
        return add_page(magnet=magnet.strip())

    @app.post("/media/add")
    def add_submit(
        magnet: Annotated[str, Form()] = "",
        torrent: Annotated[str, Form()] = "",
        name: Annotated[str, Form()] = "",
        mode: Annotated[str, Form()] = MEDIA,
    ):
        magnet, mode = magnet.strip(), mode if mode in MODES else MEDIA
        tags = qbit.TAG_MEDIA if mode == MEDIA else None
        try:
            data = decode_torrent(torrent)
            if data is not None:
                qbit.torrent_hash(data)  # rejects files that are not torrents before qBittorrent sees them
        except (ValueError, qbit.QbitError) as e:
            return add_page(magnet, name, mode, str(e), 422)
        if not magnet and data is None:
            return add_page(magnet, name, mode, "Paste a magnet link or choose a .torrent file.", 422)
        if magnet and not magnet.startswith("magnet:?"):
            return add_page(magnet, name, mode, "That is not a magnet link.", 422)
        try:
            client = qb()
            if data is not None:
                client.add_file(data, name.strip() or "upload.torrent", tags=tags)
                shown = name.strip() or "the torrent"
            else:
                client.add_magnet(magnet, tags=tags)
                shown = "the magnet link"
            if tags:
                organizer.select_files(client)
        except (media_settings.NotConfigured, qbit.QbitError) as e:
            return add_page(magnet, name, mode, str(e), 502)
        detail = "Video and subtitle files are selected once metadata arrives; it will be filed from here when complete." if mode == MEDIA else "Plain torrent: it stays in downloads/."
        return downloads(toast("success", f"Added {shown}", detail))

    # ---------- filing ----------

    @app.post("/media/file")
    async def file_submit(request: Request):
        form = {k: v for k, v in (await request.form()).items() if isinstance(v, str)}
        try:
            item = organizer.find(qb(), root(), form.get("id") or "")
        except (media_settings.NotConfigured, organizer.PlanError) as e:
            return downloads(toast("error", "Not filed", str(e)), 422)
        view = organizer.edited(item, root(), form)
        try:
            ops = organizer.plan(view, root())
        except organizer.PlanError as e:
            view.error = str(e)
            return downloads(edited=view, status_code=422)
        if form.get("action") != "file":
            return downloads(edited=view)
        try:
            organizer.apply(qb(), item, ops, root())
        except (OSError, qbit.QbitError) as e:
            view.error = str(e)
            return downloads(edited=view, status_code=502)
        return downloads(toast("success", f"Filed {item.name}", "; ".join(dst for _, dst in ops)))

    @app.post("/media/ignore")
    def ignore_submit(id: Annotated[str, Form()]):
        try:
            organizer.ignore(qb(), organizer.find(qb(), root(), id))
        except (media_settings.NotConfigured, organizer.PlanError, qbit.QbitError) as e:
            return downloads(toast("error", "Not ignored", str(e)), 422)
        return downloads(toast("success", "Ignored", "The torrent keeps seeding and is left out of the queue."))

    # ---------- channels ----------

    def channels_page(notice=None, status_code=200, *, q="", form=None, results=None):
        with connection() as conn:
            return render(
                "media_channels.html", channels=channels.list_channels(conn), status=channels.status(conn), q=q, results=results,
                form=form or {"backfill": True}, tmdb=tmdb_client is not None, toast=notice, status_code=status_code,
            )

    @app.get("/media/channels")
    def channels_index(q: str = "", imdb: str = "", name: str = "", tmdb_id: str = "", pattern: str = ""):
        q, imdb, tmdb_id = q.strip(), imdb.strip(), tmdb_id.strip()
        results, error = None, None
        try:
            if q and tmdb_client is not None:
                results = tmdb_client.search_tv(q)
            if tmdb_id.isdigit() and not imdb and tmdb_client is not None:
                digits = tmdb_client.imdb_of(tmdb_id)
                imdb = f"tt{digits}" if digits else ""
                if not imdb:
                    error = toast("error", "No IMDb id on TMDB", "Enter it by hand.")
        except tmdb.TmdbError as e:
            error = toast("error", "TMDB failed", str(e))
        form = {"imdb": imdb, "name": name.strip(), "tmdb_id": tmdb_id, "pattern": pattern, "case_sensitive": False, "backfill": True}
        return channels_page(error, q=q, form=form, results=results)

    @app.post("/media/channels")
    def channel_add(
        name: Annotated[str, Form()] = "",
        imdb: Annotated[str, Form()] = "",
        tmdb_id: Annotated[str, Form()] = "",
        pattern: Annotated[str, Form()] = "",
        case_sensitive: Annotated[str, Form()] = "",
        backfill: Annotated[str, Form()] = "",
    ):
        form = {"imdb": imdb, "name": name, "tmdb_id": tmdb_id, "pattern": pattern, "case_sensitive": bool(case_sensitive), "backfill": bool(backfill)}
        with connection() as conn:
            try:
                row = channels.add_channel(conn, name, imdb, pattern, bool(case_sensitive), tmdb_id=int(tmdb_id) if tmdb_id.strip().isdigit() else None)
            except channels.ChannelError as e:
                return channels_page(toast("error", "Not saved", str(e)), 422, form=form)
            if not backfill:
                return channels_page(toast("success", f"Watching {row['name']}", "The first poll only records what is already out; later matches are downloaded."))
            try:
                out = channels.poll(conn, env, qb(), settings, only=row["id"], force=True)
            except media_settings.NotConfigured as e:
                return channels_page(toast("error", f"Watching {row['name']}", f"Nothing downloaded: {e}"))
        added = ", ".join(a["title"] for a in out["added"]) or "no matching releases yet"
        errors = " Errors: " + "; ".join(out["errors"]) if out["errors"] else ""
        return channels_page(toast("error" if out["errors"] else "success", f"Watching {row['name']}", f"Added: {added}.{errors}"))

    @app.post("/media/channels/preview")
    def channel_preview(imdb: Annotated[str, Form()] = "", pattern: Annotated[str, Form()] = "", case_sensitive: Annotated[str, Form()] = ""):
        digits = eztv.imdb_digits(imdb)
        if digits is None:
            return render("_media_preview.html", releases=None, error="Pick a show or enter an IMDb id first.")
        try:
            releases = channels.preview(eztv.fetch(digits), pattern, bool(case_sensitive)) if pattern.strip() else [{**t, "match": False} for t in eztv.fetch(digits)]
        except (eztv.EztvError, channels.ChannelError) as e:
            return render("_media_preview.html", releases=None, error=str(e))
        return render("_media_preview.html", releases=releases, error=None, matched=sum(1 for r in releases if r["match"]))

    @app.post("/media/channels/{channel_id}/delete")
    def channel_delete(channel_id: int):
        with connection() as conn:
            try:
                row = channels.delete_channel(conn, channel_id)
            except channels.ChannelError as e:
                raise HTTPException(404, str(e)) from e
        return channels_page(toast("success", f"Removed {row['name']}"))

    @app.post("/media/channels/poll")
    def channel_poll():
        with connection() as conn:
            try:
                out = channels.poll(conn, env, qb(), settings)
            except media_settings.NotConfigured as e:
                return channels_page(toast("error", "Not polled", str(e)), 422)
        added = ", ".join(a["title"] for a in out["added"]) or "nothing new"
        return channels_page(toast("error" if out["errors"] else "success", "Polled", f"Added: {added}." + (" Errors: " + "; ".join(out["errors"]) if out["errors"] else "")))

    # ---------- library ----------

    def with_links(files):
        now = time.time()
        return [{**f, "public": links.public_url(settings, get_key(), f["rel"], now) if settings.public_url else "", "vpn": links.vpn_url(settings, f["rel"])} for f in files]

    @app.get("/media/library")
    def library_index():
        try:
            base = root()
        except media_settings.NotConfigured as e:
            return render("media_library.html", movies=[], shows=[], error=str(e))
        return render("media_library.html", movies=library.movies(base), shows=library.shows(base), error=None)

    @app.get("/media/library/movie/{name}")
    def library_movie(name: str):
        try:
            found = library.movie(root(), name)
        except (media_settings.NotConfigured, library.NotFound) as e:
            not_found(e)
        return render("media_movie.html", movie={**found, "files": with_links(found["files"])}, ttl_hours=media_settings.LINK_TTL_SECONDS // 3600)

    @app.get("/media/library/tv/{name}")
    def library_show(name: str):
        try:
            found = library.show(root(), name)
        except (media_settings.NotConfigured, library.NotFound) as e:
            not_found(e)
        seasons = [{**s, "episodes": with_links(s["episodes"])} for s in found["seasons"]]
        return render("media_show.html", show={**found, "seasons": seasons}, ttl_hours=media_settings.LINK_TTL_SECONDS // 3600)

    def image(path):
        if path is None:
            return Response(PLACEHOLDER, media_type="image/svg+xml", headers={"Cache-Control": PLACEHOLDER_CACHE})
        return FileResponse(path, media_type=artwork.IMAGE_TYPE, headers={"Cache-Control": IMAGE_CACHE})

    @app.get("/media/art/{kind}/{name}")
    def art(kind: str, name: str):
        if kind not in (artwork.MOVIE, artwork.SHOW) or "/" in name:
            not_found()
        return image(artwork.poster(settings.cache, tmdb_client, kind, name))

    @app.get("/media/art/season/{name}/{number}")
    def season_art(name: str, number: int):
        if "/" in name:
            not_found()
        return image(artwork.poster(settings.cache, tmdb_client, artwork.SEASON, name, number))

    @app.get("/media/thumb/{rel:path}")
    def thumb(rel: str):
        try:
            return image(artwork.thumbnail(settings.cache, root(), rel))
        except media_settings.NotConfigured:
            return image(None)

    @app.get(links.FILE_PREFIX + "{rel:path}")
    def media_file(rel: str, exp: str = "", sig: str = ""):
        # Verified here as well as in the Guard: local mode has no group check, and the Guard only vouches for the group.
        if not links.verify(get_key(), rel, exp, sig):
            raise HTTPException(403, "the link is invalid or has expired")
        try:
            full = library.safe_rel(root(), rel)
        except media_settings.NotConfigured as e:
            raise HTTPException(404, str(e)) from e
        if full is None:
            not_found()
        return FileResponse(full, media_type=VIDEO_TYPES.get(full.suffix.lower(), "application/octet-stream"))


def public_check(cfg, env):
    """Guard hook: admits validly signed /media/file/ requests without Remote-Groups."""
    return links.verifier(key_loader(media_settings.load(cfg, env)))
