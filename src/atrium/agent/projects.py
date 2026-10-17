from .. import notion_api
from . import context, threads

SCHEMA = """
CREATE TABLE IF NOT EXISTS projects (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    instructions TEXT NOT NULL DEFAULT '',
    effort TEXT NOT NULL,
    context_limit INTEGER NOT NULL,
    wiki_hub_id TEXT,
    archived INTEGER NOT NULL DEFAULT 0,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL
);
"""
NAME_CHARS = 80
INSTRUCTION_CHARS = 20_000

LIST_SQL = (
    "SELECT p.*, "
    "(SELECT COUNT(*) FROM threads t WHERE t.project_id = p.id AND t.kind = 'chat' AND t.archived = 0) AS thread_count, "
    "(SELECT MAX(t.updated_at) FROM threads t WHERE t.project_id = p.id AND t.kind = 'chat') AS last_activity "
    "FROM projects p"
)


class ProjectError(ValueError):
    pass


def init(conn):
    conn.executescript(SCHEMA)
    context.add_columns(conn, "threads", (("project_id", "INTEGER"),))
    conn.execute("CREATE INDEX IF NOT EXISTS threads_project ON threads (project_id, archived, updated_at)")


def clean(name, instructions, effort, context_limit, wiki_hub_id=""):
    name = (name or "").strip()
    if not name:
        raise ProjectError("Give the project a name.")
    if len(name) > NAME_CHARS:
        raise ProjectError(f"The name is limited to {NAME_CHARS} characters.")
    instructions = (instructions or "").strip()
    if len(instructions) > INSTRUCTION_CHARS:
        raise ProjectError(f"Instructions are limited to {INSTRUCTION_CHARS} characters.")
    try:
        effort, context_limit = threads.clean_effort(effort), threads.clean_context_limit(context_limit)
    except threads.OptionError as e:
        raise ProjectError(str(e)) from e
    hub = (wiki_hub_id or "").strip()
    if hub:
        found = notion_api.ids_in_text(hub)
        if not found:
            raise ProjectError("The wiki hub must be a Notion page id or URL.")
        hub = found[0]
    return {"name": name, "instructions": instructions, "effort": effort, "context_limit": context_limit, "wiki_hub_id": hub or None}


def create(conn, now, **fields):
    values = clean(**fields)
    cur = conn.execute(
        "INSERT INTO projects (name, instructions, effort, context_limit, wiki_hub_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
        (values["name"], values["instructions"], values["effort"], values["context_limit"], values["wiki_hub_id"], now, now),
    )
    return cur.lastrowid


def update(conn, project_id, now, **fields):
    values = clean(**fields)
    cur = conn.execute(
        "UPDATE projects SET name = ?, instructions = ?, effort = ?, context_limit = ?, wiki_hub_id = ?, updated_at = ? WHERE id = ?",
        (values["name"], values["instructions"], values["effort"], values["context_limit"], values["wiki_hub_id"], now, project_id),
    )
    return cur.rowcount > 0


def get(conn, project_id):
    row = conn.execute(LIST_SQL + " WHERE p.id = ?", (project_id,)).fetchone()
    return dict(row) if row else None


def list_projects(conn, archived=False):
    rows = conn.execute(LIST_SQL + " WHERE p.archived = ? ORDER BY COALESCE(last_activity, p.updated_at) DESC, p.id DESC", (int(archived),))
    return [dict(r) for r in rows]


def set_archived(conn, project_id, value):
    return conn.execute("UPDATE projects SET archived = ? WHERE id = ?", (int(value), project_id)).rowcount > 0


def set_hub(conn, project_id, page_id, now):
    return conn.execute("UPDATE projects SET wiki_hub_id = ?, updated_at = ? WHERE id = ?", (page_id, now, project_id)).rowcount > 0


def move_thread(conn, thread_id, project_id):
    """project_id None takes the thread out of its project; an archived or unknown project is refused."""
    if project_id is not None and not conn.execute("SELECT 1 FROM projects WHERE id = ? AND archived = 0", (project_id,)).fetchone():
        raise ProjectError("unknown project")
    return conn.execute("UPDATE threads SET project_id = ? WHERE id = ? AND kind = 'chat'", (project_id, thread_id)).rowcount > 0


def hub_markdown(project):
    lines = [f"Durable knowledge for the project “{project['name']}”: decisions, status, open questions and links."]
    if project["instructions"]:
        lines += ["", "## Project brief", "", project["instructions"]]
    return "\n".join(lines)


def system_block(conn, thread):
    """Per-project prompt text, appended after the cached prefix; None when the thread has no project or nothing to say."""
    if not thread.get("project_id"):
        return None
    project = get(conn, thread["project_id"])
    if project is None:
        return None
    parts = []
    if project["instructions"]:
        parts.append(f"This thread belongs to the operator's project “{project['name']}”. Follow the project's instructions:\n<project_instructions>\n{project['instructions']}\n</project_instructions>")
    if project["wiki_hub_id"]:
        parts.append(
            f"The project's wiki hub page has id {project['wiki_hub_id']}. Keep it updated with durable project knowledge "
            "(decisions, status, open questions, sources) as you learn it: load the wiki-capture skill (`/skill:wiki-capture`) and follow it, "
            "updating that hub page or pages under it rather than creating duplicates."
        )
    return "\n\n".join(parts) or None
