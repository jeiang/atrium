import hashlib
from collections import Counter

SAMPLE_SUBJECTS = 5
TOP_LABELS = 5


def digest(*parts):
    h = hashlib.sha256()
    for part in parts:
        h.update(str(part).encode())
        h.update(b"\x00")
    return h.hexdigest()[:16]


def message_view(row):
    return {
        "id": row["id"],
        "subject": row["subject"],
        "from_name": row["from_name"],
        "from_addr": row["from_addr"],
        "date_ts": row["date_ts"],
    }


def top_counts(values, limit=TOP_LABELS):
    counts = Counter(v for v in values if v)
    ranked = sorted(counts.items(), key=lambda kv: (-kv[1], kv[0]))[:limit]
    return [{"label": label, "count": n} for label, n in ranked]


def sample_subjects(rows, limit=SAMPLE_SUBJECTS):
    seen, out = set(), []
    for r in sorted(rows, key=lambda r: (-r["date_ts"], r["id"])):
        subject = (r["subject"] or "").strip()
        if subject and subject not in seen:
            seen.add(subject)
            out.append(subject[:120])
        if len(out) >= limit:
            break
    return out


def messages_by_ids(conn, ids):
    rows = []
    ids = [*ids]
    for start in range(0, len(ids), 500):
        chunk = ids[start : start + 500]
        marks = ",".join("?" * len(chunk))
        rows.extend(conn.execute(f"SELECT * FROM messages WHERE id IN ({marks})", chunk).fetchall())
    return sorted(rows, key=lambda r: r["id"])
