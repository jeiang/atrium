import json
import re
from datetime import UTC, datetime, timedelta

from . import embed
from .http import ServiceUnavailable

RRF_K = 60
BRANCH_LIMIT = 50
MAX_TERMS = 12
SNIPPET_TOKENS = 24
SNIPPET_CHARS = 220
START, END = "\x02", "\x03"
TOKEN = re.compile(r'"([^"]*)"|([^\W_]+)')
WORD = re.compile(r"[^\W_]+")
TRUTHY = {"1", "true", "on", "yes"}

BASE_SELECT = (
    "SELECT m.id, m.account, COALESCE(c.folder, m.folder, m.location) AS folder, m.from_addr, m.from_name, "
    "m.subject, m.date_ts, m.seen, m.flagged, (m.att_types != '[]') AS has_attachment "
    "FROM messages m LEFT JOIN corrections c ON c.message_id = m.id"
)


def fts_query(text):
    terms = []
    for m in TOKEN.finditer(text or ""):
        phrase, word = m.groups()
        if word:
            terms.append(f'"{word}"*')
        else:
            words = WORD.findall(phrase)
            if words:
                terms.append('"' + " ".join(words) + '"')
    return " ".join(terms[:MAX_TERMS])


def rrf(rankings, k=RRF_K):
    scores = {}
    best = {}
    for ranking in rankings:
        for rank, item in enumerate(ranking, 1):
            scores[item] = scores.get(item, 0.0) + 1.0 / (k + rank)
            best[item] = min(best.get(item, rank), rank)
    return sorted(scores.items(), key=lambda kv: (-kv[1], best[kv[0]], kv[0]))


def _flag(value):
    if isinstance(value, str):
        return value.strip().lower() in TRUTHY
    return bool(value)


def _text(value):
    return (value or "").strip() or None


def _timestamp(value, end_of_day=False):
    if value in (None, ""):
        return None
    if isinstance(value, int | float):
        return float(value)
    day = datetime.strptime(str(value).strip(), "%Y-%m-%d").replace(tzinfo=UTC)
    if end_of_day:
        day += timedelta(days=1)
    return day.timestamp()


def normalize_filters(filters):
    filters = filters or {}
    return {
        "account": _text(filters.get("account")),
        "folder": _text(filters.get("folder")),
        "from": _text(filters.get("from")),
        "date_from": _timestamp(filters.get("date_from")),
        "date_to": _timestamp(filters.get("date_to"), end_of_day=True),
        "has_attachment": _flag(filters.get("has_attachment")),
        "unread": _flag(filters.get("unread")),
        "flagged": _flag(filters.get("flagged")),
    }


def filter_sql(f):
    clauses = ["m.gone_at IS NULL"]
    params = {}
    if f["account"]:
        clauses.append("m.account = :account")
        params["account"] = f["account"]
    if f["folder"]:
        clauses.append(
            "(COALESCE(c.folder, m.folder) = :folder OR instr('|' || m.location || '|', '|' || :folder || '|') > 0)"
        )
        params["folder"] = f["folder"]
    if f["from"]:
        clauses.append(
            "(instr(lower(m.from_addr), lower(:from)) > 0 OR instr(lower(m.from_name), lower(:from)) > 0)"
        )
        params["from"] = f["from"]
    if f["date_from"] is not None:
        clauses.append("m.date_ts >= :date_from")
        params["date_from"] = f["date_from"]
    if f["date_to"] is not None:
        clauses.append("m.date_ts < :date_to")
        params["date_to"] = f["date_to"]
    if f["has_attachment"]:
        clauses.append("m.att_types != '[]'")
    if f["unread"]:
        clauses.append("m.seen = 0")
    if f["flagged"]:
        clauses.append("m.flagged = 1")
    return " AND ".join(clauses), params


def snippet_parts(marked):
    parts = []
    for chunk in marked.split(START):
        highlighted, sep, plain = chunk.partition(END)
        if sep:
            parts.append((highlighted, True))
            if plain:
                parts.append((plain, False))
        elif chunk:
            parts.append((chunk, False))
    return parts


def lexical(conn, match, f, limit=BRANCH_LIMIT):
    where, params = filter_sql(f)
    sql = (
        f"SELECT m.id, snippet(message_fts, 2, '{START}', '{END}', '…', {SNIPPET_TOKENS}) AS snip "
        "FROM message_fts JOIN messages m ON m.id = message_fts.rowid "
        f"LEFT JOIN corrections c ON c.message_id = m.id WHERE message_fts MATCH :match AND {where} "
        "ORDER BY bm25(message_fts, 5.0, 2.0, 1.0) LIMIT :limit"
    )
    return [(r["id"], r["snip"]) for r in conn.execute(sql, {**params, "match": match, "limit": limit})]


def semantic(conn, vector, f, limit=BRANCH_LIMIT):
    where, params = filter_sql(f)
    total = conn.execute("SELECT COUNT(*) FROM message_vec").fetchone()[0]
    if total == 0:
        return []
    blob = embed.serialize(vector)
    k = 400 if _narrow(f) else limit * 4
    while True:
        k = min(k, total, embed.KNN_MAX)
        knn = "SELECT rowid, distance FROM message_vec WHERE embedding MATCH :vec AND k = :k"
        if f["account"]:
            knn += " AND account = :account"
        hits = conn.execute(knn, {"vec": blob, "k": k, **({"account": f["account"]} if f["account"] else {})}).fetchall()
        ids = [h["rowid"] for h in hits]
        allowed = {
            r["id"]
            for r in conn.execute(
                f"{BASE_SELECT} WHERE m.id IN (SELECT value FROM json_each(:ids)) AND {where}",
                {**params, "ids": json.dumps(ids)},
            )
        }
        ranked = [h["rowid"] for h in hits if h["rowid"] in allowed]
        if len(ranked) >= limit or k >= total or len(hits) < k:
            return ranked[:limit]
        if k == embed.KNN_MAX:
            return exact_semantic(conn, blob, where, params, limit)
        k *= 4


def exact_semantic(conn, blob, where, params, limit):
    rows = conn.execute(
        "SELECT v.rowid FROM message_vec v WHERE v.rowid IN "
        f"(SELECT m.id FROM messages m LEFT JOIN corrections c ON c.message_id = m.id WHERE {where}) "
        "ORDER BY vec_distance_cosine(v.embedding, :vec) LIMIT :limit",
        {**params, "vec": blob, "limit": limit},
    )
    return [r["rowid"] for r in rows]


def _narrow(f):
    return (
        any(f[k] for k in ("folder", "from", "has_attachment", "unread", "flagged"))
        or f["date_from"] is not None
        or f["date_to"] is not None
    )


def body_head(conn, ids):
    if not ids:
        return {}
    rows = conn.execute(
        "SELECT rowid, substr(body, 1, ?) AS head FROM message_fts WHERE rowid IN (SELECT value FROM json_each(?))",
        (SNIPPET_CHARS, json.dumps(list(ids))),
    )
    return {r["rowid"]: " ".join((r["head"] or "").split()) for r in rows}


def hydrate(conn, ids):
    if not ids:
        return {}
    rows = conn.execute(
        f"{BASE_SELECT} WHERE m.id IN (SELECT value FROM json_each(?))", (json.dumps(list(ids)),)
    )
    return {r["id"]: dict(r) for r in rows}


def recent(conn, f, limit):
    where, params = filter_sql(f)
    rows = conn.execute(
        f"{BASE_SELECT} WHERE {where} ORDER BY m.date_ts DESC, m.id DESC LIMIT :limit", {**params, "limit": limit}
    ).fetchall()
    heads = body_head(conn, [r["id"] for r in rows])
    return [_hit(dict(r), None, None, None, heads.get(r["id"], ""), None) for r in rows]


def _hit(info, score, lex_rank, sem_rank, head, marked):
    parts = snippet_parts(marked) if marked else ([(head, False)] if head else [])
    return {
        **info,
        "has_attachment": bool(info["has_attachment"]),
        "seen": bool(info["seen"]),
        "flagged": bool(info["flagged"]),
        "score": score,
        "lexical_rank": lex_rank,
        "semantic_rank": sem_rank,
        "snippet": "".join(text for text, _ in parts),
        "snippet_parts": parts,
    }


def search(conn, cfg, query, filters=None, limit=20, embed_texts=embed.embed_texts):
    f = normalize_filters(filters)
    query = (query or "").strip()
    if not query:
        return {"hits": recent(conn, f, limit), "semantic": False, "error": None, "filters": f}
    match = fts_query(query)
    lex = lexical(conn, match, f) if match else []
    error = None
    sem = []
    try:
        vector = embed_texts(cfg.embed_url, [embed.query_text(query)])[0]
        sem = semantic(conn, vector, f)
        available = True
    except (ServiceUnavailable, ValueError, KeyError) as e:
        available = False
        error = str(e)
    lex_rank = {mid: i for i, (mid, _) in enumerate(lex, 1)}
    marked = dict(lex)
    sem_rank = {mid: i for i, mid in enumerate(sem, 1)}
    fused = rrf([[mid for mid, _ in lex], sem])[:limit]
    ids = [mid for mid, _ in fused]
    info = hydrate(conn, ids)
    heads = body_head(conn, [mid for mid in ids if mid not in marked])
    hits = [
        _hit(info[mid], score, lex_rank.get(mid), sem_rank.get(mid), heads.get(mid, ""), marked.get(mid))
        for mid, score in fused
        if mid in info
    ]
    return {"hits": hits, "semantic": available, "error": error, "filters": f}
