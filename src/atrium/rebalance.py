import logging
import operator
import re
from array import array
from collections import Counter, defaultdict
from dataclasses import dataclass

from . import llm, mailops, proposals
from .db import now
from .http import ServiceUnavailable
from .scanutil import digest, message_view, messages_by_ids, sample_subjects, top_counts

log = logging.getLogger("atrium.rebalance")

SPLIT = "rebalance_split"
MERGE = "rebalance_merge"
MIN_FOLDER = 40
MIN_GROUP = 20
MIN_SHARE = 0.10
MERGE_MIN_GROUP = 3
MERGE_MIN_OTHERS = 10
MERGE_MIN_SHARE = 0.9
RUNS_REQUIRED = 2
RUN_GAP_SECONDS = 14 * 86400
CLUSTER_KS = (2, 3, 4)
CLUSTER_ITERATIONS = 15
CLUSTER_MARGIN = 0.04
CLUSTER_MIN_EMBEDDED = 0.8
TOP_TERMS = 5
NAME_MAX_CHARS = 40
NAME_TOKENS = 24
STOPWORDS = frozenset(
    "the and for you your with from this that have has are was our new now not but out all can get will "
    "just more one any its off per via".split()
)
NAME_SYSTEM = (
    "You name a mail folder. You get the existing parent folder, the top sender domains and common subject words "
    "of a group of messages that will move into a new folder. Answer a short, plain folder name of one to three "
    "words that describes what the messages are, without the sender's name unless that is what they are about."
)
NAME_SCHEMA = {
    "type": "object",
    "properties": {"name": {"type": "string", "maxLength": NAME_MAX_CHARS}},
    "required": ["name"],
    "additionalProperties": False,
}
LEVELS = (
    ("domain", lambda r: r["from_domain"]),
    ("addr", lambda r: r["from_addr"]),
    ("tmpl", lambda r: f"{r['from_addr']}|{r['subject_tmpl']}" if r["from_addr"] and r["subject_tmpl"] else ""),
)
MERGE_LEVELS = (
    ("addr", lambda r: r["from_addr"]),
    ("tmpl", lambda r: f"{r['from_addr']}|{r['subject_tmpl']}" if r["from_addr"] and r["subject_tmpl"] else ""),
)

SCHEMA = """
CREATE TABLE IF NOT EXISTS rebalance_candidates (
    fingerprint TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    account TEXT NOT NULL,
    folder TEXT NOT NULL,
    first_ts REAL NOT NULL,
    last_ts REAL NOT NULL,
    runs INTEGER NOT NULL
);
"""


def init_schema(conn):
    conn.executescript(SCHEMA)


@dataclass
class Group:
    key: str
    ids: list


@dataclass
class Candidate:
    kind: str
    account: str
    folder: str
    level: str
    groups: list
    into: str | None = None

    @property
    def fingerprint(self):
        keys = "\x01".join(sorted(g.key for g in self.groups))
        return f"{self.kind}:{self.account}:{self.folder}:{self.into or ''}:{self.level}:{digest(keys)}"


def folder_rows(conn):
    rows = conn.execute(
        "SELECT * FROM messages WHERE gone_at IS NULL AND sent = 0 AND in_inbox = 0 "
        "AND folder IS NOT NULL AND folder != '' ORDER BY id"
    ).fetchall()
    by_folder = defaultdict(list)
    for r in rows:
        by_folder[(r["account"], r["folder"])].append(r)
    return by_folder


def big_groups(rows, keyer, total):
    groups = defaultdict(list)
    for r in rows:
        key = keyer(r)
        if key:
            groups[key].append(r["id"])
    big = [Group(k, ids) for k, ids in groups.items() if len(ids) >= MIN_GROUP and len(ids) >= MIN_SHARE * total]
    return sorted(big, key=lambda g: (-len(g.ids), g.key))


def sender_split(rows):
    for level, keyer in LEVELS:
        big = big_groups(rows, keyer, len(rows))
        if len(big) >= 2:
            return level, big[1:]
    return None


def unit_vectors(conn, ids):
    found = {}
    for start in range(0, len(ids), 500):
        chunk = ids[start : start + 500]
        marks = ",".join("?" * len(chunk))
        for r in conn.execute(f"SELECT rowid, embedding FROM message_vec WHERE rowid IN ({marks})", chunk):
            found[r["rowid"]] = array("f", r["embedding"])
    return found


def dot(a, b):
    return sum(map(operator.mul, a, b))


def centroid(members):
    total = [sum(col) for col in zip(*members, strict=True)]
    norm = sum(x * x for x in total) ** 0.5 or 1.0
    return array("f", (x / norm for x in total))


def kmeans(vectors, k):
    mean = centroid(vectors)
    centers = [min(vectors, key=lambda v: dot(v, mean))]
    while len(centers) < k:
        centers.append(min(vectors, key=lambda v: max(dot(v, c) for c in centers)))
    assign = []
    for _ in range(CLUSTER_ITERATIONS):
        sims = [[dot(v, c) for c in centers] for v in vectors]
        fresh = [max(range(k), key=lambda j: s[j]) for s in sims]
        if fresh == assign:
            break
        assign = fresh
        for j in range(k):
            members = [v for v, a in zip(vectors, assign, strict=True) if a == j]
            if members:
                centers[j] = centroid(members)
    margin = 0.0
    for s, a in zip(sims, assign, strict=True):
        margin += s[a] - max((x for j, x in enumerate(s) if j != a), default=0.0)
    return assign, margin / len(vectors)


def embedding_split(conn, rows):
    ids = [r["id"] for r in rows]
    vectors = unit_vectors(conn, ids)
    if len(vectors) < CLUSTER_MIN_EMBEDDED * len(rows):
        return None
    order = [i for i in ids if i in vectors]
    points = [vectors[i] for i in order]
    best = max((kmeans(points, k) for k in CLUSTER_KS), key=lambda r: r[1])
    assign, margin = best
    if margin < CLUSTER_MARGIN:
        return None
    clusters = defaultdict(list)
    for i, a in zip(order, assign, strict=True):
        clusters[a].append(i)
    by_id = {r["id"]: r for r in rows}
    big = []
    for members in clusters.values():
        if len(members) >= MIN_GROUP and len(members) >= MIN_SHARE * len(rows):
            domains = top_counts((by_id[i]["from_domain"] for i in members), 3)
            big.append(Group("|".join(sorted(d["label"] for d in domains)), members))
    big.sort(key=lambda g: (-len(g.ids), g.key))
    return ("embed", big[1:]) if len(big) >= 2 else None


def split_candidates(conn, by_folder):
    found = []
    for (account, folder), rows in sorted(by_folder.items()):
        if len(rows) < MIN_FOLDER:
            continue
        split = sender_split(rows) or embedding_split(conn, rows)
        if split:
            level, groups = split
            found.append(Candidate(SPLIT, account, folder, level, groups))
    return found


def merge_candidates(by_folder):
    found = []
    everything = [(key, r) for key, rows in by_folder.items() for r in rows]
    for level, keyer in MERGE_LEVELS:
        placed = defaultdict(Counter)
        for (account, folder), r in everything:
            key = keyer(r)
            if key:
                placed[(account, key)][folder] += 1
        for (account, folder), rows in sorted(by_folder.items()):
            if len(rows) < MIN_FOLDER:
                continue
            groups = defaultdict(list)
            for r in rows:
                key = keyer(r)
                if key:
                    groups[key].append(r["id"])
            for key, ids in sorted(groups.items()):
                counts = placed[(account, key)]
                others = {f: n for f, n in counts.items() if f != folder}
                total = sum(others.values())
                if len(ids) < MERGE_MIN_GROUP or total < MERGE_MIN_OTHERS:
                    continue
                into, top = max(others.items(), key=lambda kv: (kv[1], kv[0]))
                if top / total >= MERGE_MIN_SHARE:
                    found.append(Candidate(MERGE, account, folder, level, [Group(key, ids)], into=into))
    claimed, unique = set(), []
    for c in found:
        ids = {i for g in c.groups for i in g.ids}
        if not ids & claimed:
            claimed |= ids
            unique.append(c)
    return unique


def detect(conn):
    by_folder = folder_rows(conn)
    merges = merge_candidates(by_folder)
    return merges + split_candidates(conn, by_folder)


def terms_of(rows):
    counts = Counter()
    for r in rows:
        words = {w for w in re.findall(r"[a-z]{3,}", r["subject_tmpl"] or "") if w not in STOPWORDS}
        counts.update(words)
    ranked = sorted(counts.items(), key=lambda kv: (-kv[1], kv[0]))[:TOP_TERMS]
    return [w for w, _ in ranked]


def clean_name(value):
    name = re.sub(r"[/|\\\x00-\x1f]+", " ", value or "")
    return re.sub(r"\s+", " ", name).strip()[:NAME_MAX_CHARS].strip()


def suggest_name(cfg, folder, domains, terms):
    prompt = (
        f"Parent folder: {folder}\nTop sender domains: {', '.join(domains)}\nCommon subject words: {', '.join(terms)}"
    )
    try:
        answer = llm.chat_json(cfg.chat_url, NAME_SYSTEM, prompt, NAME_SCHEMA)
        name = clean_name(answer.get("name"))
    except (ServiceUnavailable, ValueError, KeyError, IndexError, TypeError, AttributeError):
        name = ""
    return name or clean_name(domains[0] if domains else "") or "Split"


def group_preview(conn, rows, name):
    return {
        "name": name,
        "count": len(rows),
        "senders": top_counts(r["from_domain"] for r in rows),
        "addresses": top_counts(r["from_addr"] for r in rows),
        "terms": terms_of(rows),
        "subjects": sample_subjects(rows),
        "messages": [message_view(r) for r in rows],
    }


def build(conn, cfg, cand, namer):
    if cand.kind == MERGE:
        group = cand.groups[0]
        rows = messages_by_ids(conn, group.ids)
        return {
            "title": f"Move {len(rows)} messages from {cand.folder} to {cand.into}",
            "payload": {
                "folder": cand.folder,
                "groups": [{"dest": cand.into, "messages": mailops.snapshots(conn, group.ids)}],
            },
            "preview": {
                "folder": cand.folder,
                "sources": [{"folder": cand.folder, "count": len(rows)}],
                "into": cand.into,
                "into_exists": True,
                "group": group_preview(conn, rows, cand.into),
            },
        }
    payload_groups, preview_groups = [], []
    for group in cand.groups:
        rows = messages_by_ids(conn, group.ids)
        info = group_preview(conn, rows, "")
        short = namer(cfg, cand.folder, [d["label"] for d in info["senders"]], info["terms"])
        dest = f"{cand.folder}/{short}"
        preview_groups.append({**info, "name": dest})
        payload_groups.append({"dest": dest, "messages": mailops.snapshots(conn, group.ids)})
    count = sum(g["count"] for g in preview_groups)
    return {
        "title": f"Split {count} messages out of {cand.folder} into {len(preview_groups)} new folders",
        "payload": {"folder": cand.folder, "groups": payload_groups},
        "preview": {"folder": cand.folder, "groups": preview_groups},
    }


def track(conn, candidates, ts):
    live = {c.fingerprint: c for c in candidates}
    conn.execute("BEGIN")
    try:
        known = {r["fingerprint"]: r for r in conn.execute("SELECT * FROM rebalance_candidates")}
        for fp in known.keys() - live.keys():
            conn.execute("DELETE FROM rebalance_candidates WHERE fingerprint = ?", (fp,))
        for fp, c in live.items():
            seen = known.get(fp)
            if seen is None:
                conn.execute(
                    "INSERT INTO rebalance_candidates (fingerprint, kind, account, folder, first_ts, last_ts, runs) "
                    "VALUES (?, ?, ?, ?, ?, ?, 1)",
                    (fp, c.kind, c.account, c.folder, ts, ts),
                )
            elif ts - seen["last_ts"] >= RUN_GAP_SECONDS:
                conn.execute(
                    "UPDATE rebalance_candidates SET runs = runs + 1, last_ts = ? WHERE fingerprint = ?", (ts, fp)
                )
        conn.execute("COMMIT")
    except BaseException:
        conn.execute("ROLLBACK")
        raise
    return {
        r["fingerprint"]: r["runs"] for r in conn.execute("SELECT fingerprint, runs FROM rebalance_candidates")
    }


def scan(conn, cfg, ts=None, namer=suggest_name):
    ts = now() if ts is None else ts
    candidates = detect(conn)
    runs = track(conn, candidates, ts)
    created = 0
    for cand in candidates:
        if runs[cand.fingerprint] < RUNS_REQUIRED:
            continue
        if proposals.known(conn, cand.fingerprint):
            continue
        built = build(conn, cfg, cand, namer)
        made = proposals.create(
            conn, cand.kind, cand.account, built["title"], built["payload"], built["preview"], cand.fingerprint
        )
        created += made is not None
    log.info("rebalance: %d candidates, %d new proposals", len(candidates), created)
    return {"candidates": len(candidates), "created": created}
