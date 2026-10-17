"""Media tools: downloads, library and channels are readable from chat, cron, code and allowlisted subagents;
adding a torrent or changing channels is chat-only and lands in the agent writes ledger with undo."""

from ...media import channels, eztv, library, links, organizer, qbit, tmdb
from ...media import settings as media_settings
from .. import writes
from ._args import bounded_int, text_arg
from .base import CHAT, CODE, CRON, READ, SUB, WRITE, NotConfigured, Tool, ToolContext, ToolError

READ_CONTEXTS = frozenset({CHAT, CRON, SUB, CODE})
WRITE_CONTEXTS = frozenset({CHAT})
UNDO_TORRENT = "media_torrent_delete"
UNDO_CHANNEL_DELETE = "media_channel_delete"
UNDO_CHANNEL_RESTORE = "media_channel_restore"
TORRENT_LIMIT = 50


def _settings(cfg, env):
    return media_settings.load(cfg, env)


def _qbit(settings):
    try:
        return qbit.Qbit(settings.need_qbit())
    except media_settings.NotConfigured as e:
        raise NotConfigured(str(e)) from e


def _root(settings):
    try:
        return settings.need_root()
    except media_settings.NotConfigured as e:
        raise NotConfigured(str(e)) from e


def _torrent(t):
    return {
        "hash": t["hash"],
        "name": t["name"],
        "state": qbit.STATE_LABELS.get(t.get("state"), t.get("state")),
        "progress_percent": round(float(t.get("progress") or 0) * 100),
        "size_bytes": t.get("size"),
        "download_bytes_per_second": t.get("dlspeed"),
        "eta_seconds": t.get("eta"),
        "kind": "media" if qbit.has_tag(t, qbit.TAG_MEDIA) else "plain",
        "filed": qbit.has_tag(t, qbit.TAG_FILED),
    }


def media_downloads(ctx: ToolContext, args: dict):
    settings = _settings(ctx.cfg, ctx.env)
    qb = _qbit(settings)
    try:
        torrents = qb.torrents()
    except qbit.QbitError as e:
        raise ToolError(str(e)) from e
    waiting = organizer.items(qb, settings.root)[0] if settings.root else []
    return {
        "torrents": [_torrent(t) for t in torrents[:TORRENT_LIMIT]],
        "waiting_to_file": [{"id": it.id, "name": it.name, "torrent": it.torrent} for it in waiting],
        "note": "Only torrents of kind media are filed into the library, from the Media downloads page.",
    }


def media_library_search(ctx: ToolContext, args: dict):
    settings = _settings(ctx.cfg, ctx.env)
    root = _root(settings)
    query = text_arg(args, "query", required=True)
    limit = bounded_int(args, "limit", 20, library.SEARCH_LIMIT)
    found = library.search(root, query, limit)
    return {
        "results": [{**r, "vpn_urls": [links.vpn_url(settings, f) for f in r["files"]]} for r in found],
        "note": "Paths are relative to the media root; VPN URLs work on the mesh. Public links are made on the Library page.",
    }


def media_channels(ctx: ToolContext, args: dict):
    return {"channels": channels.list_channels(ctx.conn), **channels.status(ctx.conn)}


def media_add_magnet(ctx: ToolContext, args: dict):
    magnet = text_arg(args, "magnet", required=True)
    media = args.get("media", True)
    if not isinstance(media, bool):
        raise ToolError("media must be true or false")
    if not magnet.startswith("magnet:?"):
        raise ToolError("magnet must be a magnet: link")
    hash_ = qbit.magnet_hash(magnet)
    if hash_ is None:
        raise ToolError("the magnet link has no v1 info hash (xt=urn:btih:...)")
    settings = _settings(ctx.cfg, ctx.env)
    qb = _qbit(settings)
    try:
        qb.add_magnet(magnet, tags=qbit.TAG_MEDIA if media else None)
        if media:
            organizer.select_files(qb)
    except qbit.QbitError as e:
        raise ToolError(str(e)) from e
    write_id = writes.record(ctx, "media_add_magnet", hash_, None, {"magnet": magnet, "media": media}, {"op": UNDO_TORRENT, "hash": hash_})
    return {"hash": hash_, "kind": "media" if media else "plain", "write_id": write_id}


def _show_from_tmdb(client, tmdb_id):
    try:
        imdb = client.imdb_of(tmdb_id)
        show = client.show(tmdb_id)
    except tmdb.TmdbError as e:
        raise ToolError(str(e)) from e
    if not imdb or not show:
        raise ToolError(f"TMDB show {tmdb_id} has no IMDb id")
    return imdb, show.get("name") or f"tt{imdb}"


def _show_from_imdb(client, imdb, name):
    if name:
        return imdb, name
    if client is None:
        return imdb, f"tt{imdb}"
    try:
        found = client.show_by_imdb(imdb)
    except tmdb.TmdbError as e:
        raise ToolError(str(e)) from e
    return imdb, (found or {}).get("name") or f"tt{imdb}"


def media_channel_add(ctx: ToolContext, args: dict):
    pattern = text_arg(args, "regex", required=True)
    case_sensitive = args.get("case_sensitive", False)
    if not isinstance(case_sensitive, bool):
        raise ToolError("case_sensitive must be true or false")
    settings = _settings(ctx.cfg, ctx.env)
    client = tmdb.from_settings(settings)
    tmdb_id = args.get("tmdb_id")
    imdb = eztv.imdb_digits(text_arg(args, "imdb_id"))
    if tmdb_id is not None:
        if isinstance(tmdb_id, bool) or not isinstance(tmdb_id, int):
            raise ToolError("tmdb_id must be an integer")
        if client is None:
            raise NotConfigured("TMDB_API_KEY is not set; give imdb_id instead")
        imdb, name = _show_from_tmdb(client, tmdb_id)
    elif imdb:
        imdb, name = _show_from_imdb(client, imdb, text_arg(args, "name"))
    else:
        raise ToolError("give imdb_id (tt1234567) or tmdb_id")
    try:
        row = channels.add_channel(ctx.conn, name, imdb, pattern, case_sensitive, tmdb_id=tmdb_id, now=ctx.now)
    except channels.ChannelError as e:
        raise ToolError(str(e)) from e
    write_id = writes.record(ctx, "media_channel_add", f"channel:{row['id']}", None, row, {"op": UNDO_CHANNEL_DELETE, "id": row["id"]})
    return {**row, "write_id": write_id, "note": "The first poll only records current releases; later matches are added."}


def media_channel_remove(ctx: ToolContext, args: dict):
    channel_id = args.get("id")
    if isinstance(channel_id, bool) or not isinstance(channel_id, int):
        raise ToolError("id must be an integer")
    try:
        row = channels.delete_channel(ctx.conn, channel_id)
    except channels.ChannelError as e:
        raise ToolError(str(e)) from e
    write_id = writes.record(ctx, "media_channel_remove", f"channel:{channel_id}", row, None, {"op": UNDO_CHANNEL_RESTORE, "row": row})
    return {"removed": channel_id, "name": row["name"], "write_id": write_id}


def undo_torrent(conn, cfg, env, info):
    settings = _settings(cfg, env)
    try:
        qb = qbit.Qbit(settings.need_qbit())
        found = qb.torrent(info["hash"])
        if found is None:
            raise writes.UndoError("the torrent is no longer in qBittorrent")
        if qbit.has_tag(found, qbit.TAG_FILED):
            raise writes.UndoError("the torrent was already filed into the library")
        qb.delete(info["hash"], files=True)
    except (qbit.QbitError, media_settings.NotConfigured) as e:
        raise writes.UndoError(str(e)) from e
    return {"deleted": info["hash"]}


def undo_channel_delete(conn, cfg, env, info):
    try:
        channels.delete_channel(conn, info["id"])
    except channels.ChannelError as e:
        raise writes.UndoError(str(e)) from e
    return {"removed": info["id"]}


def undo_channel_restore(conn, cfg, env, info):
    try:
        channels.restore_channel(conn, info["row"])
    except channels.ChannelError as e:
        raise writes.UndoError(str(e)) from e
    return {"restored": info["row"]["id"]}


writes.register_undoer(UNDO_TORRENT, undo_torrent)
writes.register_undoer(UNDO_CHANNEL_DELETE, undo_channel_delete)
writes.register_undoer(UNDO_CHANNEL_RESTORE, undo_channel_restore)

TOOLS = [
    Tool(
        name="media_downloads",
        description="List torrents in qBittorrent (name, state, progress, size, speed, ETA, media or plain) and the completed media torrents and import entries waiting to be filed.",
        input_schema={"type": "object", "properties": {}, "additionalProperties": False},
        handler=media_downloads,
        effect=READ,
        contexts=READ_CONTEXTS,
    ),
    Tool(
        name="media_library_search",
        description="Search the media library (movies and TV episodes) by words in the title or file name; returns paths and VPN URLs.",
        input_schema={
            "type": "object",
            "properties": {"query": {"type": "string"}, "limit": {"type": "integer", "minimum": 1, "maximum": library.SEARCH_LIMIT}},
            "required": ["query"],
            "additionalProperties": False,
        },
        handler=media_library_search,
        effect=READ,
        contexts=READ_CONTEXTS,
    ),
    Tool(
        name="media_channels",
        description="List the EZTV channels (show, IMDb id, title regex, case sensitivity) with the last poll time and errors.",
        input_schema={"type": "object", "properties": {}, "additionalProperties": False},
        handler=media_channels,
        effect=READ,
        contexts=READ_CONTEXTS,
    ),
    Tool(
        name="media_add_magnet",
        description="Add a magnet link to qBittorrent. media=true (default) tags it for filing into the library with automatic video/subtitle selection; false adds a plain torrent. Undo deletes the torrent and its data unless it was already filed.",
        input_schema={
            "type": "object",
            "properties": {"magnet": {"type": "string"}, "media": {"type": "boolean"}},
            "required": ["magnet"],
            "additionalProperties": False,
        },
        handler=media_add_magnet,
        effect=WRITE,
        contexts=WRITE_CONTEXTS,
    ),
    Tool(
        name="media_channel_add",
        description="Watch a show on EZTV: give imdb_id (tt1234567) or tmdb_id (needs TMDB) plus a regex over release titles; case_sensitive defaults to false. New matches are downloaded automatically after the first poll. Undo removes the channel.",
        input_schema={
            "type": "object",
            "properties": {
                "imdb_id": {"type": "string"},
                "tmdb_id": {"type": "integer"},
                "regex": {"type": "string"},
                "case_sensitive": {"type": "boolean"},
                "name": {"type": "string", "description": "Display name; defaults to the TMDB title or the IMDb id."},
            },
            "required": ["regex"],
            "additionalProperties": False,
        },
        handler=media_channel_add,
        effect=WRITE,
        contexts=WRITE_CONTEXTS,
    ),
    Tool(
        name="media_channel_remove",
        description="Remove an EZTV channel by id (see media_channels). Undo restores it.",
        input_schema={"type": "object", "properties": {"id": {"type": "integer"}}, "required": ["id"], "additionalProperties": False},
        handler=media_channel_remove,
        effect=WRITE,
        contexts=WRITE_CONTEXTS,
    ),
]
