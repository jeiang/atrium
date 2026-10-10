import logging
import math

import sqlite_vec

from .config import EMBED_BATCH, EMBED_BODY_CHARS, EMBED_DIM
from .http import post_json

log = logging.getLogger("atrium.embed")

MODEL = "embeddinggemma"


def document_text(subject, name, addr, body):
    return f"title: {subject or 'none'} | text: From: {name} <{addr}>\n\n{(body or '')[:EMBED_BODY_CHARS]}"


def query_text(query):
    return f"task: search result | query: {query}"


def embed_texts(url, texts):
    reply = post_json(f"{url}/embeddings", {"model": MODEL, "input": texts})
    data = sorted(reply["data"], key=lambda d: d.get("index", 0))
    vectors = [d["embedding"] for d in data]
    if len(vectors) != len(texts) or any(len(v) != EMBED_DIM for v in vectors):
        raise ValueError(f"embedding response shape mismatch: expected {len(texts)}x{EMBED_DIM}")
    return [normalize(v) for v in vectors]


def normalize(vector):
    norm = math.sqrt(sum(x * x for x in vector)) or 1.0
    return [x / norm for x in vector]


def serialize(vector):
    return sqlite_vec.serialize_float32(vector)


def message_document(conn, message_id):
    row = conn.execute(
        "SELECT m.subject, m.from_name, m.from_addr, f.body FROM messages m "
        "JOIN message_fts f ON f.rowid = m.id WHERE m.id = ?",
        (message_id,),
    ).fetchone()
    return document_text(row["subject"], row["from_name"], row["from_addr"], row["body"])


def embed_pending(conn, url, limit=None):
    done = 0
    while limit is None or done < limit:
        rows = conn.execute(
            "SELECT id, account FROM messages WHERE embedded = 0 AND gone_at IS NULL ORDER BY id LIMIT ?",
            (EMBED_BATCH,),
        ).fetchall()
        if not rows:
            break
        vectors = embed_texts(url, [message_document(conn, r["id"]) for r in rows])
        conn.execute("BEGIN")
        try:
            for row, vector in zip(rows, vectors, strict=True):
                conn.execute("DELETE FROM message_vec WHERE rowid = ?", (row["id"],))
                conn.execute(
                    "INSERT INTO message_vec (rowid, account, embedding) VALUES (?, ?, ?)",
                    (row["id"], row["account"], serialize(vector)),
                )
                conn.execute("UPDATE messages SET embedded = 1 WHERE id = ?", (row["id"],))
            conn.execute("COMMIT")
        except BaseException:
            conn.execute("ROLLBACK")
            raise
        done += len(rows)
        log.info("embedded %d", done)
    return done


def reindex(conn, url):
    conn.execute("DELETE FROM message_vec")
    conn.execute("UPDATE messages SET embedded = 0")
    return embed_pending(conn, url)
