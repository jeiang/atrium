import time
from datetime import datetime
from typing import Annotated

from fastapi import Form, HTTPException
from fastapi.responses import RedirectResponse

from ..agent import scheduler


def register(app, deps):
    render, connection, toast = deps.render, deps.connection, deps.toast

    def view(conn):
        rows = scheduler.list_jobs(conn)
        for job in rows:
            zone = scheduler.zone(job["tz"])
            job["next_due_text"] = datetime.fromtimestamp(job["next_due"], zone).strftime("%Y-%m-%d %H:%M %Z") if job["next_due"] else None
            job["last_started_text"] = datetime.fromtimestamp(job["last_started"], zone).strftime("%Y-%m-%d %H:%M") if job["last_started"] else None
        return rows

    def show(notice=None, status_code=200):
        with connection() as conn:
            scheduler.ensure_nightly(conn, scheduler.tz_name(deps.env), time.time())
            return render("schedules.html", jobs=view(conn), tz=scheduler.tz_name(deps.env), toast=notice, status_code=status_code)

    def attempt(action, success):
        try:
            with connection() as conn:
                action(conn)
        except scheduler.ScheduleError as e:
            return show(toast("error", "Not saved", str(e)), 422)
        return show(toast("success", success))

    def load(conn, job_id):
        job = scheduler.get_job(conn, job_id)
        if job is None or job["cancelled"]:
            raise HTTPException(404, "unknown schedule")
        return job

    @app.get("/schedules")
    def schedules_page():
        return show()

    @app.post("/schedules")
    def schedule_new(
        name: Annotated[str, Form()] = "",
        prompt: Annotated[str, Form()] = "",
        schedule: Annotated[str, Form()] = "",
    ):
        def create(conn):
            scheduler.create_job(
                conn, name=name, prompt=prompt, schedule=schedule, tz=scheduler.tz_name(deps.env), now=time.time(), enforce=False
            )

        return attempt(create, "Schedule created")

    @app.post("/schedules/{job_id}")
    def schedule_edit(
        job_id: int,
        name: Annotated[str, Form()] = "",
        prompt: Annotated[str, Form()] = "",
        schedule: Annotated[str, Form()] = "",
    ):
        def edit(conn):
            job = load(conn, job_id)
            fixed = job["kind"] == scheduler.NIGHTLY
            scheduler.update_job(
                conn, job_id, time.time(), name=None if fixed else name, prompt=None if fixed else prompt, schedule=schedule, enforce=False
            )

        return attempt(edit, "Schedule saved")

    @app.post("/schedules/{job_id}/toggle")
    def schedule_toggle(job_id: int):
        def toggle(conn):
            job = load(conn, job_id)
            scheduler.update_job(conn, job_id, time.time(), enabled=not job["enabled"], enforce=False)

        return attempt(toggle, "Schedule updated")

    @app.post("/schedules/{job_id}/cancel")
    def schedule_cancel(job_id: int):
        def cancel(conn):
            load(conn, job_id)
            scheduler.cancel_job(conn, job_id)

        return attempt(cancel, "Schedule cancelled")
