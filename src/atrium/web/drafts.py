from typing import Annotated

from fastapi import Form, HTTPException, Request

from .. import drafts, smtp
from ..config import ACCOUNTS
from .deps import is_partial

STATUS_FILTERS = (drafts.DRAFT, drafts.UNKNOWN, drafts.SENT, drafts.DISCARDED)


def register(app, deps):
    cfg, render, connection, toast = deps.cfg, deps.render, deps.connection, deps.toast

    def configured():
        return {a: cfg.credential(a) is not None for a in ACCOUNTS}

    def with_warnings(conn, found):
        for d in found:
            d["unexpected"] = drafts.unexpected_recipients(conn, cfg, d)
        return found

    def show(status, status_code=200, notice=None):
        with connection() as conn:
            found = with_warnings(conn, drafts.list_drafts(conn, status or None))
        return render("drafts.html", status_code=status_code, drafts=found, status=status, statuses=STATUS_FILTERS, toast=notice, configured=configured())

    def card(draft_id, status_code=200, notice=None):
        with connection() as conn:
            row = drafts.get(conn, draft_id)
            if row is None:
                raise HTTPException(404, "unknown draft")
            (d,) = with_warnings(conn, [dict(row)])
        return render("_draft.html", status_code=status_code, d=d, partial=True, toast=notice, configured=configured())

    def reply(request, draft_id, status, status_code=200, notice=None):
        if is_partial(request):
            return card(draft_id, status_code, notice)
        return show(status, status_code, notice)

    def check_status(status):
        if status and status not in (*STATUS_FILTERS, drafts.SENDING):
            raise HTTPException(422, "unknown status")

    @app.get("/drafts")
    def drafts_page(status: str = drafts.DRAFT):
        check_status(status)
        return show(status)

    @app.get("/drafts/{draft_id}")
    def draft_page(request: Request, draft_id: int):
        with connection() as conn:
            row = drafts.get(conn, draft_id)
            if row is None:
                raise HTTPException(404, "unknown draft")
            (d,) = with_warnings(conn, [dict(row)])
        if is_partial(request):
            return render("_draft_preview.html", d=d, configured=configured())
        return render("drafts.html", drafts=[d], status=d["status"], statuses=STATUS_FILTERS, configured=configured())

    @app.post("/drafts/{draft_id}")
    def drafts_update(
        request: Request,
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
                return reply(request, draft_id, drafts.DRAFT, 422, toast("error", "Not saved", str(e)))
        return reply(request, draft_id, drafts.DRAFT, notice=toast("success", "Saved", "Draft updated."))

    @app.post("/drafts/{draft_id}/discard")
    def drafts_discard(request: Request, draft_id: int):
        with connection() as conn:
            try:
                drafts.discard(conn, draft_id)
            except drafts.DraftError as e:
                return reply(request, draft_id, drafts.DRAFT, 409, toast("error", "Not discarded", str(e)))
        return reply(request, draft_id, drafts.DRAFT, notice=toast("success", "Discarded", "Draft discarded."))

    @app.post("/drafts/{draft_id}/send")
    def drafts_send(request: Request, draft_id: int, updated_at: Annotated[str, Form()] = ""):
        with connection() as conn:
            try:
                try:
                    reviewed_at = float(updated_at)
                except ValueError:
                    raise drafts.DraftError("draft changed, review again") from None
                result = drafts.send(conn, cfg, draft_id, reviewed_at)
            except (drafts.DraftError, smtp.SendError) as e:
                return reply(request, draft_id, drafts.DRAFT, 409, toast("error", "Not sent", str(e)))
        detail = result["filed"] + (f"; refused: {', '.join(result['refused'])}" if result["refused"] else "")
        return reply(request, draft_id, drafts.DRAFT, notice=toast("success", "Sent", detail))

    @app.post("/drafts/{draft_id}/resolve")
    def drafts_resolve(request: Request, draft_id: int, outcome: Annotated[str, Form()] = ""):
        verdict = {"sent": drafts.SENT, "draft": drafts.DRAFT}.get(outcome)
        if verdict is None:
            raise HTTPException(422, "outcome must be sent or draft")
        with connection() as conn:
            try:
                drafts.resolve_unknown(conn, draft_id, verdict)
            except drafts.DraftError as e:
                return reply(request, draft_id, drafts.UNKNOWN, 409, toast("error", "Not updated", str(e)))
        return reply(request, draft_id, drafts.UNKNOWN, notice=toast("success", "Updated", f"Draft marked {verdict}."))
