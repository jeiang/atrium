import json

from fastapi import HTTPException, Request

from ..agent import tools as _tools  # noqa: F401  (importing registers every undoer)
from ..agent import writes

KIND_FILTERS = ("nightly", "chat")
LIMIT = 200
SNIPPET = 800


def _snippet(raw):
    if not raw:
        return ""
    text = json.dumps(json.loads(raw), indent=1, ensure_ascii=False)
    return text if len(text) <= SNIPPET else text[:SNIPPET] + "…"


def list_writes(conn, kind=""):
    rows = conn.execute(
        "SELECT w.*, t.kind AS thread_kind FROM agent_writes w LEFT JOIN threads t ON t.id = w.thread_id "
        "WHERE (? = '' OR t.kind = ?) ORDER BY w.id DESC LIMIT ?",
        (kind, kind, LIMIT),
    )
    return [
        {**dict(r), "before": _snippet(r["before_json"]), "after": _snippet(r["after_json"]), "can_undo": bool(r["undo_json"])}
        for r in rows
    ]


def register(app, deps):
    cfg, render, connection, toast = deps.cfg, deps.render, deps.connection, deps.toast

    def show(kind, status_code=200, notice=None):
        with connection() as conn:
            found = list_writes(conn, kind)
        return render("changes.html", status_code=status_code, writes=found, kind=kind, kinds=KIND_FILTERS, toast=notice)

    @app.get("/changes")
    def changes_page(kind: str = ""):
        if kind and kind not in KIND_FILTERS:
            raise HTTPException(422, "unknown kind")
        return show(kind)

    @app.post("/changes/{write_id}/undo")
    def changes_undo(request: Request, write_id: int, kind: str = ""):
        if kind and kind not in KIND_FILTERS:
            kind = ""
        with connection() as conn:
            try:
                writes.undo(conn, cfg, deps.env, write_id)
            except writes.UndoError as e:
                return show(kind, 409, toast("error", "Not undone", str(e)))
        return show(kind, notice=toast("success", "Undone", f"Change {write_id} was reverted."))
