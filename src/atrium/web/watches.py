import time
from datetime import datetime
from typing import Annotated

from fastapi import Form, HTTPException

from .. import birthdays, watches
from ..agent import scheduler

BIRTHDAY_WINDOW_DAYS = 60


def register(app, deps):
    cfg, render, connection, toast = deps.cfg, deps.render, deps.connection, deps.toast

    def show(notice=None, status_code=200):
        now = time.time()
        today = datetime.fromtimestamp(now, scheduler.zone(scheduler.tz_name(deps.env))).date()
        with connection() as conn:
            return render(
                "watches.html",
                renewals=watches.list_renewals(conn, today),
                packages=watches.list_packages(conn, now, active_only=True),
                birthdays=birthdays.upcoming_for(conn, cfg, today, BIRTHDAY_WINDOW_DAYS),
                toast=notice,
                status_code=status_code,
            )

    def found(ok, what):
        if not ok:
            raise HTTPException(404, f"unknown {what}")

    @app.get("/watches")
    def watches_page():
        return show()

    @app.post("/watches/renewals/{renewal_id}/dismiss")
    def renewal_dismiss(renewal_id: int):
        with connection() as conn:
            found(watches.dismiss_renewal(conn, renewal_id), "renewal")
        return show(toast("success", "Dismissed"))

    @app.post("/watches/packages/{package_id}/dismiss")
    def package_dismiss(package_id: int):
        with connection() as conn:
            found(watches.dismiss_package(conn, package_id), "package")
        return show(toast("success", "Dismissed"))

    @app.post("/watches/birthdays/dismiss")
    def birthday_dismiss(key: Annotated[str, Form()], date: Annotated[str, Form()]):
        with connection() as conn:
            try:
                birthdays.dismiss(conn, key, date)
            except ValueError as e:
                raise HTTPException(422, "date must be YYYY-MM-DD") from e
        return show(toast("success", "Dismissed", "Hidden until next year."))
