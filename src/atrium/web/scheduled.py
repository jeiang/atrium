import time
from datetime import datetime
from typing import Annotated

from fastapi import Form, HTTPException, Request
from fastapi.responses import RedirectResponse

from ..agent import scheduler
from .deps import is_partial

HISTORY = 30
JOB_HISTORY = 10


def register(app, deps):
    render, connection, toast = deps.render, deps.connection, deps.toast

    def stamp(epoch, zone, fmt="%a %d %b %H:%M"):
        return datetime.fromtimestamp(epoch, zone).strftime(fmt) if epoch else None

    def context(conn, notice=None):
        tz = scheduler.tz_name(deps.env)
        scheduler.ensure_nightly(conn, tz, time.time())
        scheduler.ensure_builtins(conn, tz, time.time())
        zone = scheduler.zone(tz)
        jobs = scheduler.list_jobs(conn)
        for job in jobs:
            job["cadence"] = scheduler.describe(job["schedule"], job["tz"])
            job["next_due_text"] = stamp(job["next_due"], zone) if job["enabled"] else None
            job["last_started_text"] = stamp(job["last_started"], zone)
        runs = scheduler.list_runs(conn, limit=HISTORY)
        for run in runs:
            run["started_text"] = stamp(run["started_at"] or run["due_at"], zone)
        return {"jobs": jobs, "runs": runs, "tz": tz, "toast": notice}

    def show(request, notice=None, status_code=200):
        with connection() as conn:
            found = context(conn, notice)
        if is_partial(request):
            return render("_scheduled_list.html", status_code=status_code, **found)
        return render("scheduled.html", status_code=status_code, **found)

    def list_reply(request, notice, status_code=200, close=True):
        reply = show(request, notice, status_code)
        reply.headers["HX-Retarget"] = "#scheduled-list"
        reply.headers["HX-Reswap"] = "outerHTML"
        if close:
            reply.headers["HX-Trigger"] = "close-dialogs"
        return reply

    def form(job=None, error=None, values=None, status_code=200):
        with connection() as conn:
            tz = scheduler.tz_name(deps.env)
            runs = scheduler.list_runs(conn, job["id"], JOB_HISTORY) if job else []
            zone = scheduler.zone(tz)
            for run in runs:
                run["started_text"] = stamp(run["started_at"] or run["due_at"], zone)
        return render("_schedule_form.html", job=job, error=error, values=values or {}, runs=runs, tz=tz, status_code=status_code)

    def attempt(request, action, success, job=None, values=None, in_form=True):
        try:
            with connection() as conn:
                action(conn)
        except scheduler.ScheduleError as e:
            if not is_partial(request):
                return show(request, toast("error", "Not saved", str(e)), 422)
            if in_form:
                return form(job, str(e), values, 422)
            return list_reply(request, toast("error", "Not saved", str(e)), 422, close=False)
        if not is_partial(request):
            return RedirectResponse("/scheduled", status_code=303)
        return list_reply(request, toast("success", success))

    def load(conn, job_id):
        job = scheduler.get_job(conn, job_id)
        if job is None or job["cancelled"]:
            raise HTTPException(404, "unknown schedule")
        return job

    @app.get("/scheduled")
    def scheduled_page(request: Request):
        return show(request)

    @app.get("/scheduled/new")
    def schedule_new_form():
        return form()

    @app.get("/scheduled/{job_id}/edit")
    def schedule_edit_form(job_id: int):
        with connection() as conn:
            job = load(conn, job_id)
        return form(job)

    @app.post("/scheduled")
    def schedule_new(
        request: Request,
        name: Annotated[str, Form()] = "",
        prompt: Annotated[str, Form()] = "",
        schedule: Annotated[str, Form()] = "",
    ):
        def create(conn):
            scheduler.create_job(
                conn, name=name, prompt=prompt, schedule=schedule, tz=scheduler.tz_name(deps.env), now=time.time(), enforce=False,
                prompt_by=scheduler.OPERATOR,
            )

        return attempt(request, create, "Schedule created", values={"name": name, "prompt": prompt, "schedule": schedule})

    @app.post("/scheduled/{job_id}")
    def schedule_edit(
        request: Request,
        job_id: int,
        name: Annotated[str, Form()] = "",
        prompt: Annotated[str, Form()] = "",
        schedule: Annotated[str, Form()] = "",
    ):
        with connection() as conn:
            job = load(conn, job_id)

        def edit(conn):
            fixed = job["kind"] in (scheduler.NIGHTLY, scheduler.BUILTIN)
            scheduler.update_job(
                conn, job_id, time.time(), name=None if fixed else name, prompt=None if fixed else prompt, schedule=schedule, enforce=False,
                prompt_by=scheduler.OPERATOR,
            )

        return attempt(request, edit, "Schedule saved", job, {"name": name, "prompt": prompt, "schedule": schedule})

    @app.post("/scheduled/{job_id}/toggle")
    def schedule_toggle(request: Request, job_id: int):
        def toggle(conn):
            job = load(conn, job_id)
            scheduler.update_job(conn, job_id, time.time(), enabled=not job["enabled"], enforce=False)

        return attempt(request, toggle, "Schedule updated", in_form=False)

    @app.post("/scheduled/{job_id}/cancel")
    def schedule_cancel(request: Request, job_id: int):
        def cancel(conn):
            load(conn, job_id)
            scheduler.cancel_job(conn, job_id)

        return attempt(request, cancel, "Schedule deleted", in_form=False)
