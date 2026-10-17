import time
from typing import Annotated

from fastapi import Form, HTTPException

from .. import openloops

SNOOZE_CHOICES = (1, 3, 7, 14)
LOOP_LIMIT = 100


def register(app, deps):
    render, connection, toast = deps.render, deps.connection, deps.toast

    def show(notice=None, status_code=200):
        now = time.time()
        with connection() as conn:
            return render(
                "openloops.html",
                owe=openloops.list_loops(conn, now, openloops.OWE_REPLY, LOOP_LIMIT),
                waiting=openloops.list_loops(conn, now, openloops.WAITING, LOOP_LIMIT),
                commitments=openloops.list_commitments(conn, openloops.OPEN, LOOP_LIMIT),
                snooze_choices=SNOOZE_CHOICES,
                toast=notice,
                status_code=status_code,
            )

    def found(ok, what):
        if not ok:
            raise HTTPException(404, f"unknown {what}")

    @app.get("/loops")
    def loops_page():
        return show()

    @app.post("/loops/{loop_id}/snooze")
    def loop_snooze(loop_id: int, days: Annotated[int, Form()] = openloops.SNOOZE_DEFAULT_DAYS):
        days = max(1, min(days, 60))
        with connection() as conn:
            found(openloops.snooze(conn, loop_id, time.time() + days * openloops.DAY), "loop")
        return show(toast("success", "Snoozed", f"Hidden for {days} day{'s' if days != 1 else ''}."))

    @app.post("/loops/{loop_id}/dismiss")
    def loop_dismiss(loop_id: int):
        with connection() as conn:
            found(openloops.dismiss(conn, loop_id), "loop")
        return show(toast("success", "Dismissed"))

    @app.post("/loops/dismiss-all")
    def loop_dismiss_all(state: Annotated[str, Form()]):
        if state not in openloops.STATES:
            raise HTTPException(400, "unknown state")
        with connection() as conn:
            count = openloops.dismiss_all(conn, state)
        return show(toast("success", "Dismissed", f"{count} loop{'s' if count != 1 else ''} cleared."))

    @app.post("/loops/commitments/{commitment_id}/{outcome}")
    def commitment_update(commitment_id: int, outcome: str):
        status = {"done": openloops.DONE, "dismiss": openloops.DISMISSED}.get(outcome)
        if status is None:
            raise HTTPException(404, "unknown action")
        with connection() as conn:
            found(openloops.set_commitment_status(conn, commitment_id, status), "commitment")
        return show(toast("success", "Marked done" if status == openloops.DONE else "Dismissed"))
