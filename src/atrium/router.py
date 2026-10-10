import json
import logging
from dataclasses import dataclass, field

from . import corrections, embed, llm
from .config import (
    ACCOUNTS,
    FOLDER_MIN_MESSAGES,
    KNN_K,
    KNN_MIN_SHARE,
    LLM_NEIGHBORS,
)
from .db import now
from .http import ServiceUnavailable
from .rules import first_match, learned_match, load_rules

log = logging.getLogger("atrium.router")

STAGES = ("correction", "operator", "learned", "knn", "llm", "fallback")


@dataclass
class Decision:
    stage: str
    action: str
    dest: str | None
    key: str | None = None
    confidence: float | None = None
    detail: dict = field(default_factory=dict)


@dataclass
class LlmContext:
    url: str
    folders: list
    catalog: str
    available: bool = True
    down: bool = False


def eligible(conn, account):
    return conn.execute(
        "SELECT m.* FROM messages m WHERE m.account = ? AND m.in_inbox = 1 AND m.seen = 1 AND m.flagged = 0 "
        "AND m.sent = 0 AND m.gone_at IS NULL "
        "AND NOT EXISTS (SELECT 1 FROM decisions d WHERE d.message_id = m.id) ORDER BY m.id",
        (account,),
    ).fetchall()


def nearest_filed(conn, row, count):
    found = conn.execute("SELECT embedding FROM message_vec WHERE rowid = ?", (row["id"],)).fetchone()
    if found is None:
        return []
    total = conn.execute(
        "SELECT COUNT(*) FROM message_vec WHERE account = ?", (row["account"],)
    ).fetchone()[0]
    k = max(count * 4, 32)
    while True:
        hits = conn.execute(
            "SELECT rowid, distance FROM message_vec WHERE embedding MATCH ? AND k = ? AND account = ?",
            (found["embedding"], min(k, max(total, 1), embed.KNN_MAX), row["account"]),
        ).fetchall()
        ids = [h["rowid"] for h in hits if h["rowid"] != row["id"]]
        info = {}
        if ids:
            marks = ",".join("?" * len(ids))
            info = {
                r["id"]: r
                for r in conn.execute(
                    f"SELECT id, folder, from_domain, subject FROM filed_messages WHERE id IN ({marks})", ids
                )
            }
        neighbors = [
            {**dict(info[h["rowid"]]), "similarity": max(1.0 - h["distance"], 0.0)}
            for h in hits
            if h["rowid"] in info
        ]
        if len(neighbors) >= count or k >= min(total, embed.KNN_MAX):
            return neighbors[:count]
        k *= 4


def vote(neighbors):
    scores = {}
    for n in neighbors:
        scores[n["folder"]] = scores.get(n["folder"], 0.0) + n["similarity"]
    total = sum(scores.values())
    if not scores or total <= 0:
        return None
    folder = max(scores, key=lambda f: (scores[f], f))
    return folder, scores[folder] / total


def llm_context(conn, account, url):
    folders = [
        r["folder"]
        for r in conn.execute(
            "SELECT folder FROM filed_messages WHERE account = ? GROUP BY folder HAVING COUNT(*) >= ? ORDER BY folder",
            (account, FOLDER_MIN_MESSAGES),
        )
    ]
    if not folders:
        return LlmContext(url, [], "", False)
    marks = ",".join("?" * len(folders))
    rows = conn.execute(
        f"SELECT folder, from_domain FROM filed_messages WHERE account = ? AND folder IN ({marks})",
        [account, *folders],
    ).fetchall()
    return LlmContext(url, folders, llm.folder_catalog(rows))


def message_body(conn, message_id):
    row = conn.execute("SELECT body FROM message_fts WHERE rowid = ?", (message_id,)).fetchone()
    return row["body"] if row else ""


def decide(conn, row, rules, ctx, judge=llm.judge_folder):
    account = row["account"]
    spec = ACCOUNTS[account]
    corrected = corrections.get_correction(conn, row["id"])
    if corrected is not None:
        return Decision(
            "correction", "file", corrected["folder"], key=corrected["source"], confidence=1.0
        )
    rule = first_match(row, rules)
    if rule is not None:
        return Decision("operator", rule.action, rule.dest, key=rule.id)
    learned = learned_match(conn, row)
    if learned is not None:
        return Decision(
            "learned",
            "file",
            learned["dest"],
            key=learned["level"],
            confidence=learned["purity"],
            detail={"n": learned["n"]},
        )
    if not row["embedded"]:
        return None
    neighbors = nearest_filed(conn, row, max(KNN_K, LLM_NEIGHBORS))
    voted = vote(neighbors[:KNN_K])
    if voted is not None and voted[1] >= KNN_MIN_SHARE:
        return Decision("knn", "file", voted[0], key="vote", confidence=voted[1])
    if neighbors and ctx.available:
        if ctx.down:
            return None
        folder, confident = judge(
            ctx.url, ctx.folders, ctx.catalog, neighbors[:LLM_NEIGHBORS], row, message_body(conn, row["id"])
        )
        if folder != llm.UNCERTAIN:
            return Decision("llm", "file", folder, key="chat", detail={"confident": confident})
    dest = spec.uncertain_dest
    return Decision("fallback", "file" if dest else "none", dest)


def record(conn, row, decision):
    conn.execute(
        "INSERT INTO decisions (message_id, account, ts, stage, key, action, dest, confidence, detail) "
        "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
        (
            row["id"],
            row["account"],
            now(),
            decision.stage,
            decision.key,
            decision.action,
            decision.dest,
            decision.confidence,
            json.dumps(decision.detail),
        ),
    )


def route_account(conn, cfg, account, judge=llm.judge_folder):
    pending = eligible(conn, account)
    if not pending:
        return 0
    rules = load_rules(conn, account)
    ctx = llm_context(conn, account, cfg.chat_url)
    decided = 0
    for row in pending:
        try:
            decision = decide(conn, row, rules, ctx, judge)
        except ServiceUnavailable as e:
            log.warning("chat unavailable, deferring %s: %s", account, e)
            ctx.down = True
            continue
        if decision is None:
            continue
        record(conn, row, decision)
        decided += 1
    return decided
