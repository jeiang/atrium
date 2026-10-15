from typing import Annotated

from fastapi import Form, HTTPException

from .. import drafts, smtp
from ..config import ACCOUNTS

STATUS_FILTERS = (drafts.DRAFT, drafts.UNKNOWN, drafts.SENT, drafts.DISCARDED)


def register(app, deps):
    cfg, render, connection, toast = deps.cfg, deps.render, deps.connection, deps.toast

    def show(status, status_code=200, notice=None):
        with connection() as conn:
            found = drafts.list_drafts(conn, status or None)
            for d in found:
                d["unexpected"] = drafts.unexpected_recipients(conn, cfg, d)
        return render(
            "drafts.html",
            status_code=status_code,
            drafts=found,
            status=status,
            statuses=STATUS_FILTERS,
            toast=notice,
            configured={a: cfg.credential(a) is not None for a in ACCOUNTS},
        )

    def check_status(status):
        if status and status not in (*STATUS_FILTERS, drafts.SENDING):
            raise HTTPException(422, "unknown status")

    @app.get("/drafts")
    def drafts_page(status: str = drafts.DRAFT):
        check_status(status)
        return show(status)

    @app.post("/drafts/{draft_id}")
    def drafts_update(
        draft_id: int,
        account: Annotated[str, Form()],
        to: Annotated[str, Form()] = "",
        cc: Annotated[str, Form()] = "",
        subject: Annotated[str, Form()] = "",
        body: Annotated[str, Form()] = "",
    ):
        with connection() as conn:
            try:
                drafts.update(conn, cfg, draft_id, {"account": account, "to": to, "cc": cc, "subject": subject, "body": body})
            except drafts.DraftError as e:
                return show(drafts.DRAFT, 422, toast("error", "Not saved", str(e)))
        return show(drafts.DRAFT, notice=toast("success", "Saved", "Draft updated."))

    @app.post("/drafts/{draft_id}/discard")
    def drafts_discard(draft_id: int):
        with connection() as conn:
            try:
                drafts.discard(conn, draft_id)
            except drafts.DraftError as e:
                return show(drafts.DRAFT, 409, toast("error", "Not discarded", str(e)))
        return show(drafts.DRAFT, notice=toast("success", "Discarded", "Draft discarded."))

    @app.post("/drafts/{draft_id}/send")
    def drafts_send(draft_id: int):
        with connection() as conn:
            try:
                result = drafts.send(conn, cfg, draft_id)
            except (drafts.DraftError, smtp.SendError) as e:
                return show(drafts.DRAFT, 409, toast("error", "Not sent", str(e)))
        detail = result["filed"] + (f"; refused: {', '.join(result['refused'])}" if result["refused"] else "")
        return show(drafts.DRAFT, notice=toast("success", "Sent", detail))

    @app.post("/drafts/{draft_id}/resolve")
    def drafts_resolve(draft_id: int, outcome: Annotated[str, Form()] = ""):
        verdict = {"sent": drafts.SENT, "draft": drafts.DRAFT}.get(outcome)
        if verdict is None:
            raise HTTPException(422, "outcome must be sent or draft")
        with connection() as conn:
            try:
                drafts.resolve_unknown(conn, draft_id, verdict)
            except drafts.DraftError as e:
                return show(drafts.UNKNOWN, 409, toast("error", "Not updated", str(e)))
        return show(drafts.UNKNOWN, notice=toast("success", "Updated", f"Draft marked {verdict}."))
