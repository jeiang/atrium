import json
import re
import time

from .builtin_skills import BUILTINS

NAME = re.compile(r"^[a-z0-9][a-z0-9_-]{0,47}$")
# `/skill:name` (omp style) or `[[skill:name]]`, in skill bodies, chat messages and schedule prompts.
REF = re.compile(r"(?<![\w/])/skill:([a-z0-9][a-z0-9_-]{0,47})|\[\[skill:([a-z0-9][a-z0-9_-]{0,47})\]\]")
DESCRIPTION_CHARS = 160
BODY_CHARS = 20000
EXPAND_MAX_SKILLS = 5
EXPAND_MAX_CHARS = 20000

SCHEMA = """
CREATE TABLE IF NOT EXISTS skills (
    name TEXT PRIMARY KEY,
    description TEXT NOT NULL,
    body TEXT NOT NULL,
    updated_at REAL NOT NULL
);
"""


def init(conn):
    conn.executescript(SCHEMA)
    now = time.time()
    for name, description, body in BUILTINS:
        conn.execute("INSERT OR IGNORE INTO skills (name, description, body, updated_at) VALUES (?, ?, ?, ?)", (name, description, body, now))


class SkillError(ValueError):
    pass


def list_skills(conn, q=""):
    return [
        dict(r)
        for r in conn.execute(
            "SELECT name, description, body, updated_at FROM skills WHERE ? = '' OR instr(lower(name || char(10) || description || char(10) || body), lower(?)) > 0 ORDER BY name",
            (q, q),
        )
    ]


def get(conn, name):
    row = conn.execute("SELECT name, description, body, updated_at FROM skills WHERE name = ?", (name,)).fetchone()
    return dict(row) if row else None


def save(conn, name, description, body, now):
    name = (name or "").strip()
    description = " ".join((description or "").split())
    body = (body or "").strip()
    if not NAME.match(name):
        raise SkillError("name must be lowercase letters, digits, - or _ (max 48 characters)")
    if not description or len(description) > DESCRIPTION_CHARS:
        raise SkillError(f"description is required, one line, at most {DESCRIPTION_CHARS} characters")
    if not body or len(body) > BODY_CHARS:
        raise SkillError(f"body is required, at most {BODY_CHARS} characters")
    conn.execute(
        "INSERT INTO skills (name, description, body, updated_at) VALUES (?, ?, ?, ?) "
        "ON CONFLICT(name) DO UPDATE SET description = excluded.description, body = excluded.body, updated_at = excluded.updated_at",
        (name, description, body, now),
    )


def delete(conn, name):
    return conn.execute("DELETE FROM skills WHERE name = ?", (name,)).rowcount > 0


def index_block(conn):
    """Stable system-prompt block listing skill names; empty when there are none."""
    rows = conn.execute("SELECT name, description FROM skills ORDER BY name").fetchall()
    if not rows:
        return ""
    return "Skills (operator-written playbooks; call load_skill(name) to read one when it applies):\n" + "\n".join(
        f"- {r['name']}: {r['description']}" for r in rows
    )


# ---- references between skills

def references(text):
    """Skill names referenced in `text`, unique, in order of first appearance."""
    return list(dict.fromkeys(m.group(1) or m.group(2) for m in REF.finditer(text or "")))


def split_known(conn, names):
    known, unknown = [], []
    for n in names:
        (known if get(conn, n) else unknown).append(n)
    return known, unknown


def references_of(conn, name):
    """{"known": [...], "unknown": [...]} for the skills `name` references (itself excluded)."""
    found = get(conn, name)
    names = [n for n in references(found["body"] if found else "") if n != name]
    known, unknown = split_known(conn, names)
    return {"known": known, "unknown": unknown}


def referenced_by(conn, name):
    return [s["name"] for s in list_skills(conn) if s["name"] != name and name in references(s["body"])]


def with_references(skills, conn):
    for s in skills:
        s["references"] = references_of(conn, s["name"])
        s["referenced_by"] = referenced_by(conn, s["name"])
    return skills


def loaded_in_thread(conn, thread_id):
    """Skills already in the model's context for this thread: expanded into a message or read with load_skill."""
    names = set()
    for row in conn.execute("SELECT skills_json FROM runs WHERE thread_id = ? AND skills_json IS NOT NULL", (thread_id,)):
        names.update(json.loads(row["skills_json"]))
    for row in conn.execute(
        "SELECT c.args_json FROM tool_calls c JOIN runs r ON r.id = c.run_id WHERE r.thread_id = ? AND c.name = 'load_skill' AND c.state = 'done'",
        (thread_id,),
    ):
        name = (json.loads(row["args_json"]) or {}).get("name")
        if isinstance(name, str):
            names.add(name)
    return names


def related(conn, name, loaded):
    """Referenced skills not yet loaded in the thread (name + description); unknown names are reported as such."""
    refs = references_of(conn, name)
    rows = [{"name": n, "description": get(conn, n)["description"]} for n in refs["known"] if n not in loaded]
    return rows, refs["unknown"]


# ---- expansion of /skill:name tokens in operator messages and job prompts

def closure(conn, names, max_skills=EXPAND_MAX_SKILLS, max_chars=EXPAND_MAX_CHARS):
    """Breadth-first over references from `names`: (skills in order, unknown names, names left out by the caps).

    Visited names are never queued twice, so reference cycles terminate."""
    queue = list(dict.fromkeys(names))
    seen = set(queue)
    chosen, unknown, skipped = [], [], []
    chars = 0
    while queue:
        name = queue.pop(0)
        found = get(conn, name)
        if found is None:
            unknown.append(name)
            continue
        if len(chosen) >= max_skills or chars + len(found["body"]) > max_chars:
            skipped.append(name)
            continue
        chosen.append(found)
        chars += len(found["body"])
        for ref in references(found["body"]):
            if ref not in seen:
                seen.add(ref)
                queue.append(ref)
    return chosen, unknown, skipped


def block(skill):
    return f'<skill name="{skill["name"]}">\n{skill["body"]}\n</skill>'


def expand(conn, text):
    """Append the bodies of skills referenced in `text` (and, transitively, in those skills) as delimited blocks.

    Tokens stay in place. Returns (text, used names, notes); unknown or capped names are explained in a note
    appended to the text so the model can see what was not expanded."""
    names = references(text)
    if not names:
        return text, [], []
    chosen, unknown, skipped = closure(conn, names)
    notes = []
    if unknown:
        notes.append("Unknown skills (no such skill is defined): " + ", ".join(unknown))
    if skipped:
        notes.append(f"Not expanded (at most {EXPAND_MAX_SKILLS} skills / {EXPAND_MAX_CHARS} characters per message; use load_skill): " + ", ".join(skipped))
    parts = [text, *(block(s) for s in chosen), *notes]
    return "\n\n".join(parts), [s["name"] for s in chosen], notes
