from collections import Counter

from . import report
from .config import ACCOUNTS
from .router import STAGES

AGREEMENTS = ("agree", "disagree", "pending", "gone")
TOP_DESTS = 15

AGREEMENT_SQL = (
    "CASE WHEN actual IS NULL THEN 'gone' WHEN actual_inbox = 1 THEN 'pending' "
    "WHEN dest IS NOT NULL AND instr('|' || actual || '|', '|' || dest || '|') > 0 THEN 'agree' "
    "ELSE 'disagree' END"
)

LATEST_SQL = f"""
SELECT d.id AS decision_id, d.ts, d.stage, d.key, d.action, d.dest, d.confidence, d.detail,
  m.id AS message_id, m.account, m.subject, m.from_addr, m.from_name, m.date_ts, m.seen, m.flagged,
  {report.ACTUAL_SQL},
  c.folder AS corrected_to
FROM decisions d JOIN messages m ON m.id = d.message_id
LEFT JOIN corrections c ON c.message_id = m.id
WHERE d.id = (SELECT MAX(x.id) FROM decisions x WHERE x.message_id = d.message_id)
"""


def triage(conn, account=None, stage=None, agreement=None, limit=50, offset=0):
    if stage is not None and stage not in STAGES:
        raise ValueError(f"unknown stage {stage!r}")
    if agreement is not None and agreement not in AGREEMENTS:
        raise ValueError(f"unknown agreement {agreement!r}")
    clauses, params = [], []
    if account:
        clauses.append("account = ?")
        params.append(account)
    if stage:
        clauses.append("stage = ?")
        params.append(stage)
    if agreement:
        clauses.append(f"{AGREEMENT_SQL} = ?")
        params.append(agreement)
    where = f" WHERE {' AND '.join(clauses)}" if clauses else ""
    base = f"SELECT *, {AGREEMENT_SQL} AS agreement FROM ({LATEST_SQL})"
    total = conn.execute(f"SELECT COUNT(*) FROM ({base}){where}", params).fetchone()[0]
    rows = conn.execute(
        f"SELECT * FROM ({base}){where} ORDER BY decision_id DESC LIMIT ? OFFSET ?", [*params, limit, offset]
    ).fetchall()
    return {
        "total": total,
        "rows": [_shape(r) for r in rows],
    }


def row(conn, message_id):
    base = f"SELECT *, {AGREEMENT_SQL} AS agreement FROM ({LATEST_SQL})"
    found = conn.execute(f"SELECT * FROM ({base}) WHERE message_id = ?", (message_id,)).fetchone()
    return _shape(found) if found else None


def _shape(r):
    return {**dict(r), "seen": bool(r["seen"]), "flagged": bool(r["flagged"]), "current": _current(r)}


def _current(row):
    if row["actual"] is None:
        return None
    return "INBOX" if row["actual_inbox"] else row["actual"]


def summary(conn, account, since=None):
    start = report.parse_since(since)
    rows = conn.execute(report.OUTCOME_SQL, (account, start)).fetchall()
    stages = Counter(r["stage"] for r in rows)
    dests = Counter(r["dest"] for r in rows if r["action"] == "file" and r["dest"])
    moved = [r for r in rows if report.outcome(r) not in ("INBOX", "(gone)")]
    agree = [r for r in moved if report.matches(r["dest"], r["actual"])]
    return {
        "account": account,
        "decisions": len(rows),
        "by_stage": {s: stages[s] for s in STAGES},
        "proposed_moves": sum(dests.values()),
        "proposed_deletes": sum(1 for r in rows if r["action"] == "delete"),
        "no_label": sum(1 for r in rows if r["action"] == "none"),
        "top_destinations": dests.most_common(TOP_DESTS),
        "still_in_inbox": sum(1 for r in rows if report.outcome(r) == "INBOX"),
        "gone": sum(1 for r in rows if report.outcome(r) == "(gone)"),
        "moved": len(moved),
        "agreed": len(agree),
        "agreement_pct": 100 * len(agree) / len(moved) if moved else None,
        "flags": report.flag_counts(conn, account, start),
    }


def summaries(conn, since=None):
    return [
        summary(conn, name, since)
        for name in ACCOUNTS
        if conn.execute("SELECT 1 FROM accounts WHERE name = ?", (name,)).fetchone()
    ]


def flag_judgments(conn, account=None, limit=50, offset=0):
    sql = (
        "SELECT f.message_id, f.account, f.ts, f.contacts_match, f.person_asking, f.deadline_or_payment_due, "
        "f.security_event, f.llm_flag, m.subject, m.from_addr, m.from_name, m.date_ts "
        "FROM flag_decisions f JOIN messages m ON m.id = f.message_id "
        "WHERE m.gone_at IS NULL AND m.in_inbox = 1 AND m.seen = 0"
    )
    params = []
    if account:
        sql += " AND f.account = ?"
        params.append(account)
    sql += " ORDER BY f.message_id DESC LIMIT ? OFFSET ?"
    return [dict(r) for r in conn.execute(sql, [*params, limit, offset])]


def folders(conn, account):
    rows = conn.execute(
        "SELECT folder, COUNT(*) AS n FROM filed_messages WHERE account = ? GROUP BY folder ORDER BY folder",
        (account,),
    )
    return [{"folder": r["folder"], "count": r["n"]} for r in rows]
