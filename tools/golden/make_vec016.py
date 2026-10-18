import os
import sys

if os.environ.get("PYTHONHASHSEED") != "0" or os.environ.get("TZ") != "UTC":
    os.environ.update(PYTHONHASHSEED="0", TZ="UTC")
    os.execv(sys.executable, [sys.executable, *sys.argv])

import hashlib
import json
import math
import shutil
import sqlite3
import tempfile
import time
from pathlib import Path

T0 = 1792080000.0
time.time = lambda: T0
time.time_ns = lambda: int(T0 * 1_000_000_000)

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "src"))

from atrium import db, embed, notion_sync
from atrium.config import EMBED_DIM

NIXPKGS = "c5c4a43b0e8056328ec4529f735cabdb8f1942bb"
WANT = {"sqlite": "3.51.2", "sqlite-vec": "v0.1.6"}
OUT = ROOT / "testdata" / "golden"
URL = "http://embed.invalid"
VEC_TABLES = ("message_vec", "notion_vec")

MESSAGES = [
    ("icloud", "Dentist appointment", "Dr Ramdeen", "frontdesk@smiles.example", "Your cleaning is booked for Tuesday at 9."),
    ("icloud", "Power bill", "T&TEC", "billing@ttec.example", "Your electricity bill of TTD 412.50 is due on the 30th."),
    ("icloud", "Flight itinerary", "Caribbean Airlines", "noreply@caribbean-airlines.example", "POS to BGI, seat 14C, departs 07:15."),
    ("gmail", "Order shipped", "Shop", "orders@shop.example", "Your order of running shoes has shipped and arrives Friday."),
    ("gmail", "Receipt for your purchase", "Grocer", "receipts@grocer.example", "Receipt: bread, rice, coffee. Total TTD 186.20."),
    ("gmail", "Team lunch", "Priya", "priya@work.example", "Lunch on Thursday at the roti shop near the office?"),
]

PAGES = [
    ("page-0001", "Travel checklist", "Home / Travel", "Passport, adapters, chargers, sunscreen.", 0),
    ("page-0002", "Garden plan", "Home / Garden", "Plant peppers and chive along the back fence.", 0),
    ("page-0003", "Medical history", "Private / Health", "Dentist every six months; allergy to penicillin.", 1),
    ("page-0004", "Budget 2026", "Private / Money", "Electricity, groceries and savings targets per month.", 1),
]

QUERIES = ["dentist cleaning", "electricity bill", "shoes order receipt"]


def raw_vector(text):
    seed = int.from_bytes(hashlib.sha256(text.encode()).digest()[:4], "big")
    return [math.sin(seed % 1009 * 0.013 + i * (0.002 + seed % 97 * 0.0001)) + ((seed >> (i % 24)) & 3) * 0.05 for i in range(EMBED_DIM)]


def fake_post_json(url, payload):
    assert url == f"{URL}/embeddings" and payload["model"] == embed.MODEL
    return {"data": [{"index": i, "embedding": raw_vector(text)} for i, text in enumerate(payload["input"])]}


def populate(conn):
    for account in ("icloud", "gmail"):
        db.ensure_account(conn, account)
        conn.execute(
            "INSERT INTO mailboxes (account, name, imap_name, role, uidvalidity, highest_uid) VALUES (?, 'INBOX', 'INBOX', 'inbox', 1, 3)",
            (account,),
        )
    boxes = {r["account"]: r["id"] for r in conn.execute("SELECT id, account FROM mailboxes")}
    uids = {"icloud": 0, "gmail": 0}
    for n, (account, subject, name, addr, body) in enumerate(MESSAGES):
        uids[account] += 1
        cur = conn.execute(
            "INSERT INTO messages (account, mailbox_id, uidvalidity, uid, msgid, digest, date_ts, in_inbox, location, "
            "from_addr, from_domain, from_name, subject, body_len, first_seen) VALUES (?, ?, 1, ?, ?, ?, ?, 1, 'inbox', ?, ?, ?, ?, ?, ?)",
            (
                account,
                boxes[account],
                uids[account],
                f"m{n}@vec016.example",
                hashlib.sha256(f"{account}{n}".encode()).hexdigest(),
                T0 - n * 3600,
                addr,
                addr.split("@")[1],
                name,
                subject,
                len(body),
                T0,
            ),
        )
        conn.execute(
            "INSERT INTO message_fts (rowid, subject, sender, body) VALUES (?, ?, ?, ?)",
            (cur.lastrowid, subject, f"{name} {addr}", body),
        )
    for pid, title, path, markdown, private in PAGES:
        conn.execute(
            "INSERT INTO notion_pages (id, title, path, markdown, private, sensitivity, synced_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
            (pid, title, path, markdown, private, "private" if private else "", T0),
        )


def knn(conn, sql, *args):
    return [{"rowid": r[0], "distance": r[1]} for r in conn.execute(sql, args)]


def shadow_tables(conn):
    rows = conn.execute("SELECT name, sql FROM sqlite_master WHERE type = 'table' AND sql IS NOT NULL ORDER BY name")
    return {r[0]: r[1] for r in rows if any(r[0].startswith(t + "_") for t in VEC_TABLES)}


def record(conn):
    out = {
        "nixpkgs": NIXPKGS,
        "sqlite": sqlite3.sqlite_version,
        "sqlite_vec": conn.execute("SELECT vec_version()").fetchone()[0],
        "python": sys.version.split()[0],
        "quick_check": conn.execute("PRAGMA quick_check").fetchone()[0],
        "shadow_tables": shadow_tables(conn),
        "embeddings": {
            table: {str(r[0]): hashlib.sha256(r[1]).hexdigest() for r in conn.execute(f"SELECT rowid, embedding FROM {table} ORDER BY rowid")}
            for table in VEC_TABLES
        },
        "partitions": {
            "message_vec": {str(r[0]): r[1] for r in conn.execute("SELECT rowid, account FROM message_vec ORDER BY rowid")},
            "notion_vec": {str(r[0]): r[1] for r in conn.execute("SELECT rowid, visibility FROM notion_vec ORDER BY rowid")},
        },
        "queries": [],
    }
    for text in QUERIES:
        blob = embed.serialize(embed.embed_texts(URL, [embed.query_text(text)])[0])
        message_knn = "SELECT rowid, distance FROM message_vec WHERE embedding MATCH ? AND k = ?"
        notion_knn = "SELECT rowid, distance FROM notion_vec WHERE embedding MATCH ? AND k = ? AND visibility = ?"
        out["queries"].append(
            {
                "text": text,
                "vector": blob.hex(),
                "message_vec": knn(conn, message_knn, blob, 4),
                "message_vec_icloud": knn(conn, message_knn + " AND account = ?", blob, 2, "icloud"),
                "message_vec_gmail": knn(conn, message_knn + " AND account = ?", blob, 3, "gmail"),
                "message_vec_exact": knn(conn, "SELECT rowid, vec_distance_cosine(embedding, ?) AS d FROM message_vec ORDER BY d, rowid", blob),
                "notion_vec_public": knn(conn, notion_knn, blob, 2, notion_sync.PUBLIC),
                "notion_vec_private": knn(conn, notion_knn, blob, 2, notion_sync.PRIVATE),
            }
        )
    return out


def main():
    work = Path(tempfile.mkdtemp(prefix="atrium-vec016-"))
    try:
        path = work / "atrium.db"
        embed.post_json = fake_post_json
        conn = db.connect(path)
        versions = {"sqlite": sqlite3.sqlite_version, "sqlite-vec": conn.execute("SELECT vec_version()").fetchone()[0]}
        if versions != WANT:
            sys.exit(f"needs {WANT} (nixpkgs {NIXPKGS}), running {versions}")
        populate(conn)
        assert embed.embed_pending(conn, URL) == len(MESSAGES)
        assert notion_sync.embed_pending(conn, URL) == len(PAGES)
        recorded = record(conn)
        conn.close()
        conn = sqlite3.connect(path)
        conn.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        conn.execute("PRAGMA journal_mode=DELETE")
        conn.close()
        shutil.copyfile(path, OUT / "atrium-vec016.db")
        (OUT / "atrium-vec016.json").write_text(json.dumps(recorded, sort_keys=True, indent=2) + "\n")
    finally:
        shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    main()
