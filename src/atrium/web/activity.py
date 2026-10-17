import json

from fastapi import HTTPException, Request
from fastapi.responses import RedirectResponse

from .. import live
from ..agent import tools as _tools  # noqa: F401  (importing registers every undoer)
from ..agent import writes
from .deps import is_partial

MAIL, ASSISTANT, MONEY, WIKI, MEDIA = "mail", "assistant", "money", "wiki", "media"
CATEGORIES = (MAIL, ASSISTANT, MONEY, WIKI, MEDIA)
PAGE_SIZE = 50
SNIPPET = 800
CATEGORY_SQL = (
    "CASE WHEN json_extract(w.undo_json, '$.op') = 'money_restore' THEN 'money' WHEN w.tool LIKE 'notion%' THEN 'wiki' "
    "WHEN w.tool LIKE 'media%' THEN 'media' ELSE 'assistant' END"
)


def _snippet(raw):
    if not raw:
        return ""
    text = json.dumps(json.loads(raw), indent=1, ensure_ascii=False)
    return text if len(text) <= SNIPPET else text[:SNIPPET] + "…"


def _write_entry(row):
    return {
        **dict(row),
        "source": "write",
        "before": _snippet(row["before_json"]),
        "after": _snippet(row["after_json"]),
        "undoable": bool(row["undo_json"]) and row["undone_at"] is None,
    }


def get_write(conn, write_id):
    row = conn.execute(
        f"SELECT w.*, t.kind AS thread_kind, {CATEGORY_SQL} AS category FROM agent_writes w LEFT JOIN threads t ON t.id = w.thread_id WHERE w.id = ?",
        (write_id,),
    ).fetchone()
    return _write_entry(row) if row else None


def timeline(conn, category="", limit=PAGE_SIZE, offset=0):
    """Mailbox auto-actions and agent/ledger writes merged newest first; the caller shows `limit` entries from `offset`."""
    want = offset + limit + 1
    entries = []
    if category in ("", MAIL):
        entries += [{**a, "source": "mail", "category": MAIL} for a in live.list_actions(conn, None, None, want, 0)]
    if category != MAIL:
        rows = conn.execute(
            f"SELECT * FROM (SELECT w.*, t.kind AS thread_kind, {CATEGORY_SQL} AS category FROM agent_writes w "
            "LEFT JOIN threads t ON t.id = w.thread_id) WHERE (? = '' OR category = ?) ORDER BY ts DESC, id DESC LIMIT ?",
            (category, category, want),
        )
        entries += [_write_entry(r) for r in rows]
    entries.sort(key=lambda e: (e["ts"], e["id"]), reverse=True)
    shown = entries[offset : offset + limit]
    return shown, offset + limit if len(entries) > offset + limit else None


def register(app, deps):
    cfg, render, connection, toast = deps.cfg, deps.render, deps.connection, deps.toast

    def check(category):
        if category and category not in CATEGORIES:
            raise HTTPException(422, "unknown category")

    @app.get("/activity")
    def activity_page(request: Request, category: str = "", offset: int = 0):
        check(category)
        offset = max(offset, 0)
        with connection() as conn:
            entries, next_offset = timeline(conn, category, PAGE_SIZE, offset)
        context = {"entries": entries, "category": category, "categories": CATEGORIES, "next_offset": next_offset, "offset": offset}
        if is_partial(request) and offset:
            return render("_activity_rows.html", **context)
        return render("activity.html", **context)

    def undone(request, entry, notice):
        if not is_partial(request):
            return RedirectResponse("/activity", status_code=303)
        return render("_activity_row.html", e=entry, toast=notice)

    @app.post("/activity/mail/{action_id}/undo")
    def undo_mail(request: Request, action_id: int):
        with connection() as conn:
            if live.get(conn, action_id) is None:
                raise HTTPException(404, "unknown action")
            try:
                live.undo(conn, cfg, action_id)
                notice = toast("success", "Undone")
            except live.UndoError as e:
                if not is_partial(request):
                    raise HTTPException(409, str(e)) from e
                notice = toast("error", "Not undone", str(e))
            found = live.get(conn, action_id)
        return undone(request, {**found, "source": "mail", "category": MAIL}, notice)

    @app.post("/activity/write/{write_id}/undo")
    def undo_write(request: Request, write_id: int):
        with connection() as conn:
            if get_write(conn, write_id) is None:
                raise HTTPException(404, "unknown write")
            try:
                writes.undo(conn, cfg, deps.env, write_id)
                notice = toast("success", "Undone", f"Change {write_id} was reverted.")
            except writes.UndoError as e:
                if not is_partial(request):
                    raise HTTPException(409, str(e)) from e
                notice = toast("error", "Not undone", str(e))
            found = get_write(conn, write_id)
        return undone(request, found, notice)
