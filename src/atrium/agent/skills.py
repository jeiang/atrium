import re

NAME = re.compile(r"^[a-z0-9][a-z0-9_-]{0,47}$")
DESCRIPTION_CHARS = 160
BODY_CHARS = 20000

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


class SkillError(ValueError):
    pass


def list_skills(conn):
    return [dict(r) for r in conn.execute("SELECT name, description, body, updated_at FROM skills ORDER BY name")]


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
