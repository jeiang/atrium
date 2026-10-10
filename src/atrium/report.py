import re
import sqlite3
import time
from collections import Counter

from .config import ACCOUNTS, ICLOUD, INBOX
from .router import STAGES

SINCE = re.compile(r"^(\d+)([hd])$")
DISAGREEMENT_LINES = 40
TOP_DESTS = 15

LIVE_COPY = (
    "(SELECT l.{column} FROM messages l WHERE l.account = m.account AND l.digest = m.digest "
    "AND l.gone_at IS NULL ORDER BY l.id DESC LIMIT 1)"
)
ACTUAL_SQL = f"{LIVE_COPY.format(column='location')} AS actual, {LIVE_COPY.format(column='in_inbox')} AS actual_inbox"

OUTCOME_SQL = f"""
SELECT d.id AS decision_id, d.stage, d.action, d.dest, m.id AS message_id, m.msgid, m.from_domain,
  {ACTUAL_SQL}
FROM decisions d JOIN messages m ON m.id = d.message_id
WHERE d.account = ? AND d.ts >= ?
ORDER BY d.id
"""


def parse_since(value, at=None):
    at = time.time() if at is None else at
    if value is None:
        return 0.0
    m = SINCE.match(value)
    if m:
        return at - int(m.group(1)) * (3600 if m.group(2) == "h" else 86400)
    return time.mktime(time.strptime(value, "%Y-%m-%d"))


def jev_decisions(path):
    if path is None or not path.exists():
        return None
    conn = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    try:
        return {str(mid).strip("<>"): folder for mid, folder in conn.execute("SELECT message_id, folder FROM decisions")}
    finally:
        conn.close()


def outcome(row):
    if row["actual"] is None:
        return "(gone)"
    if row["actual_inbox"]:
        return INBOX
    return row["actual"]


def matches(dest, actual):
    return dest is not None and dest in actual.split("|")


def account_section(conn, cfg, account, since, jev):
    rows = conn.execute(OUTCOME_SQL, (account, since)).fetchall()
    out = [f"== {account} ==", f"decisions: {len(rows)}"]
    stages = Counter(r["stage"] for r in rows)
    out.append("by stage: " + ", ".join(f"{s}={stages[s]}" for s in STAGES))
    dests = Counter(r["dest"] for r in rows if r["action"] == "file" and r["dest"])
    deletes = sum(1 for r in rows if r["action"] == "delete")
    out.append(f"proposed moves: {sum(dests.values())}, proposed deletes: {deletes}, no label: {sum(1 for r in rows if r['action'] == 'none')}")
    for dest, n in dests.most_common(TOP_DESTS):
        out.append(f"  {n:5d}  {dest}")
    moved = [r for r in rows if outcome(r) not in (INBOX, "(gone)")]
    inbox = sum(1 for r in rows if outcome(r) == INBOX)
    gone = sum(1 for r in rows if outcome(r) == "(gone)")
    agree = [r for r in moved if matches(r["dest"], r["actual"])]
    out.append(
        f"outcome: still in inbox={inbox}, gone={gone}, moved={len(moved)}, "
        f"proposal equals actual={len(agree)}"
        + (f" ({100 * len(agree) / len(moved):.1f}%)" if moved else "")
    )
    if jev is not None and account == ICLOUD:
        out.extend(jev_section(rows, moved, jev))
    else:
        out.extend(disagreement_lines("disagreements (proposed -> actual)", [(r, r["actual"]) for r in moved if not matches(r["dest"], r["actual"])]))
    out.extend(flag_section(conn, account, since))
    return out


def jev_section(rows, moved, jev):
    both = [r for r in rows if r["msgid"] in jev]
    out = [f"jev comparison: decided by both={len(both)}"]
    if both:
        same = sum(1 for r in both if jev[r["msgid"]] == r["dest"])
        out.append(f"  atrium == jev: {same} ({100 * same / len(both):.1f}%)")
        settled = [r for r in both if outcome(r) not in (INBOX, "(gone)")]
        if settled:
            a = sum(1 for r in settled if matches(r["dest"], r["actual"]))
            j = sum(1 for r in settled if jev[r["msgid"]] == r["actual"])
            out.append(f"  of {len(settled)} settled: atrium == actual {a}, jev == actual {j}")
    disagreeing = [
        (r, f"{r['actual']} (jev: {jev.get(r['msgid'], '-')})")
        for r in moved
        if not matches(r["dest"], r["actual"])
    ]
    out.extend(disagreement_lines("disagreements (proposed -> actual)", disagreeing))
    return out


def disagreement_lines(title, pairs):
    out = [f"{title}: {len(pairs)}"]
    grouped = Counter((r["from_domain"], r["dest"] or "-", actual) for r, actual in pairs)
    for (domain, proposed, actual), n in grouped.most_common(DISAGREEMENT_LINES):
        out.append(f"  {n:4d}  {domain}: {proposed} -> {actual}")
    return out


def flag_counts(conn, account, since):
    rows = conn.execute(
        "SELECT contacts_match, llm_flag FROM flag_decisions WHERE account = ? AND ts >= ?", (account, since)
    ).fetchall()
    judged = [r for r in rows if r["llm_flag"] is not None]
    contacts = sum(r["contacts_match"] for r in rows)
    llm = sum(r["llm_flag"] for r in judged)
    return {
        "considered": len(rows),
        "contacts": contacts,
        "llm": llm,
        "judged": len(judged),
        "both": sum(1 for r in judged if r["contacts_match"] and r["llm_flag"]),
        "contacts_only": sum(1 for r in judged if r["contacts_match"] and not r["llm_flag"]),
        "llm_only": sum(1 for r in judged if r["llm_flag"] and not r["contacts_match"]),
    }


def flag_section(conn, account, since):
    n = flag_counts(conn, account, since)
    return [
        f"flags (unread inbox): considered={n['considered']}, contacts would-flag={n['contacts']}, "
        f"9B flag={n['llm']} of {n['judged']} judged, both={n['both']}, contacts-only={n['contacts_only']}, "
        f"9B-only={n['llm_only']}"
    ]


def build(conn, cfg, since=None):
    start = parse_since(since)
    jev = jev_decisions(cfg.jev_db)
    lines = []
    for account in ACCOUNTS:
        if conn.execute("SELECT 1 FROM accounts WHERE name = ?", (account,)).fetchone() is None:
            continue
        lines.extend(account_section(conn, cfg, account, start, jev))
        lines.append("")
    return "\n".join(lines).rstrip() + "\n"
