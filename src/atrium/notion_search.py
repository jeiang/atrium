import json

from . import embed
from . import search as mail_search
from .http import ServiceUnavailable
from .notion_api import ids_in_text, normalize_id
from .notion_markdown import plain
from .notion_sync import PATH_SEPARATOR, PRIVATE, PUBLIC

ACCOUNT = "notion"
BRANCH_LIMIT = 50
RELATED_LIMIT = 10
SNIPPET_CHARS = 220
HEAD_SOURCE_CHARS = 1000

HIT_SELECT = (
    "SELECT rid, id, title, path, url, last_edited_time, edited_ts, private FROM notion_pages "
    "WHERE in_trash = 0 AND rid IN (SELECT value FROM json_each(?))"
)


def path_parts(path):
    return [{"id": None, "title": t} for t in (path or "").split(PATH_SEPARATOR) if t]


def pageref(row):
    return {
        "id": row["id"],
        "title": row["title"],
        "url": row["url"],
        "path": path_parts(row["path"]),
        "private": bool(row["private"]),
    }


def property_text(prop):
    kind = prop.get("type")
    value = prop.get(kind)
    if kind in ("title", "rich_text"):
        return "".join(i.get("plain_text", "") for i in value or [])
    if kind in ("select", "status"):
        return (value or {}).get("name", "")
    if kind == "multi_select":
        return ", ".join(v.get("name", "") for v in value or [])
    if kind == "date":
        return " → ".join(x for x in ((value or {}).get("start"), (value or {}).get("end")) if x)
    if kind == "relation":
        return f"{len(value)} linked" if value else ""
    if kind == "people":
        return ", ".join(v.get("name", "") for v in value or [])
    if value is None or isinstance(value, dict | list):
        return ""
    return str(value)


def property_rows(properties):
    rows = [{"name": n, "value": property_text(p)} for n, p in json.loads(properties).items() if p.get("type") != "title"]
    return [r for r in rows if r["value"]]


def page_view(conn, page_id):
    page = get_page(conn, page_id)
    if page is None:
        return None
    return {
        **pageref(page),
        "edited_ts": page["edited_ts"],
        "properties": property_rows(page["properties"]),
        "markdown": page["markdown"],
        "in_trash": bool(page["in_trash"]),
    }


def visibilities(include_private):
    return (PUBLIC, PRIVATE) if include_private else (PUBLIC,)


def knn(conn, blob, k, include_private):
    total = conn.execute("SELECT COUNT(*) FROM notion_vec").fetchone()[0]
    if total == 0:
        return []
    k = min(k, total, embed.KNN_MAX)
    rows = []
    for visibility in visibilities(include_private):
        rows += conn.execute(
            "SELECT rowid, distance FROM notion_vec WHERE embedding MATCH ? AND k = ? AND visibility = ?",
            (blob, k, visibility),
        ).fetchall()
    rows.sort(key=lambda r: r["distance"])
    return rows


def lexical(conn, match, include_private, limit=BRANCH_LIMIT):
    sql = (
        f"SELECT p.rid, snippet(notion_fts, 2, '{mail_search.START}', '{mail_search.END}', '…', {mail_search.SNIPPET_TOKENS}) AS snip "
        "FROM notion_fts JOIN notion_pages p ON p.rid = notion_fts.rowid "
        "WHERE notion_fts MATCH :match AND p.in_trash = 0 AND (:private = 1 OR p.private = 0) "
        "ORDER BY bm25(notion_fts, 5.0, 2.0, 1.0) LIMIT :limit"
    )
    return [
        (r["rid"], r["snip"])
        for r in conn.execute(sql, {"match": match, "private": int(include_private), "limit": limit})
    ]


def semantic(conn, vector, include_private, limit=BRANCH_LIMIT):
    rows = knn(conn, embed.serialize(vector), limit, include_private)
    return [r["rowid"] for r in rows][:limit]


def hydrate(conn, rids):
    if not rids:
        return {}
    return {r["rid"]: dict(r) for r in conn.execute(HIT_SELECT, (json.dumps(list(rids)),))}


def page_titles(conn, texts):
    ids = ids_in_text(" ".join(texts))
    if not ids:
        return {}
    rows = conn.execute(
        "SELECT id, title FROM notion_pages WHERE in_trash = 0 AND id IN (SELECT value FROM json_each(?))",
        (json.dumps(ids),),
    )
    return {r["id"]: r["title"] for r in rows}


def heads(conn, rids):
    if not rids:
        return {}
    rows = conn.execute(
        "SELECT rid, substr(markdown, 1, ?) AS head FROM notion_pages WHERE rid IN (SELECT value FROM json_each(?))",
        (HEAD_SOURCE_CHARS, json.dumps(list(rids))),
    ).fetchall()
    titles = page_titles(conn, [r["head"] or "" for r in rows])
    return {r["rid"]: plain(r["head"], titles)[:SNIPPET_CHARS] for r in rows}


def hit(info, score, lex_rank, sem_rank, head, marked):
    parts = mail_search.snippet_parts(marked) if marked else ([(head, False)] if head else [])
    return {
        **pageref(info),
        "account": ACCOUNT,
        "subject": info["title"],
        "folder": info["path"],
        "last_edited_time": info["last_edited_time"],
        "edited_ts": info["edited_ts"],
        "date_ts": info["edited_ts"],
        "from_addr": "",
        "from_name": "",
        "seen": True,
        "flagged": False,
        "has_attachment": False,
        "score": score,
        "lexical_rank": lex_rank,
        "semantic_rank": sem_rank,
        "snippet": "".join(text for text, _ in parts),
        "snippet_parts": parts,
    }


def recent(conn, include_private, limit):
    rows = conn.execute(
        "SELECT rid, id, title, path, url, last_edited_time, edited_ts, private FROM notion_pages "
        "WHERE in_trash = 0 AND (? = 1 OR private = 0) ORDER BY edited_ts DESC, rid DESC LIMIT ?",
        (int(include_private), limit),
    ).fetchall()
    head = heads(conn, [r["rid"] for r in rows])
    return [hit(dict(r), None, None, None, head.get(r["rid"], ""), None) for r in rows]


def search(conn, cfg, query, include_private=False, limit=20, embed_texts=embed.embed_texts):
    query = (query or "").strip()
    filters = {"account": ACCOUNT, "include_private": bool(include_private)}
    if not query:
        return {"hits": recent(conn, include_private, limit), "semantic": False, "error": None, "filters": filters}
    match = mail_search.fts_query(query)
    lex = lexical(conn, match, include_private) if match else []
    error = None
    sem = []
    try:
        vector = embed_texts(cfg.embed_url, [embed.query_text(query)])[0]
        sem = semantic(conn, vector, include_private)
        available = True
    except (ServiceUnavailable, ValueError, KeyError) as e:
        available = False
        error = str(e)
    lex_rank = {rid: i for i, (rid, _) in enumerate(lex, 1)}
    titles = page_titles(conn, [snip for _, snip in lex])
    marked = {rid: plain(snip, titles) for rid, snip in lex}
    sem_rank = {rid: i for i, rid in enumerate(sem, 1)}
    fused = mail_search.rrf([[rid for rid, _ in lex], sem])[:limit]
    rids = [rid for rid, _ in fused]
    info = hydrate(conn, rids)
    head = heads(conn, [rid for rid in rids if rid not in marked])
    hits = [
        hit(info[rid], score, lex_rank.get(rid), sem_rank.get(rid), head.get(rid, ""), marked.get(rid))
        for rid, score in fused
        if rid in info
    ]
    return {"hits": hits, "semantic": available, "error": error, "filters": filters}


def get_page(conn, page_id):
    row = conn.execute("SELECT * FROM notion_pages WHERE id = ?", (normalize_id(page_id),)).fetchone()
    return dict(row) if row is not None else None


def related(conn, page_id, limit=RELATED_LIMIT):
    page = get_page(conn, page_id)
    if page is None:
        return []
    allow_private = bool(page["private"])
    linked = [normalize_id(i) for i in json.loads(page["links"])]
    backlinks = [
        r["id"]
        for r in conn.execute(
            "SELECT p.id FROM notion_pages p, json_each(p.links) j WHERE j.value = ? AND p.id != ?",
            (page["id"], page["id"]),
        )
    ]
    out = []
    taken = {page["id"]}
    for rid in [*linked, *backlinks]:
        if rid in taken:
            continue
        row = conn.execute(
            "SELECT rid, id, title, path, url, private FROM notion_pages WHERE id = ? AND in_trash = 0", (rid,)
        ).fetchone()
        if row is None or (row["private"] and not allow_private):
            continue
        taken.add(rid)
        out.append({**pageref(row), "linked": True, "score": 1.0})
    vec = conn.execute("SELECT embedding FROM notion_vec WHERE rowid = ?", (page["rid"],)).fetchone()
    if vec is not None:
        near = knn(conn, vec["embedding"], limit + len(taken) + 1, allow_private)
        info = hydrate(conn, [n["rowid"] for n in near])
        for n in near:
            row = info.get(n["rowid"])
            if row is None or row["id"] in taken:
                continue
            taken.add(row["id"])
            out.append({**pageref(row), "linked": False, "score": max(0.0, 1.0 - n["distance"])})
    return out[:limit]
