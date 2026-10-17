import time
from typing import Annotated

from fastapi import Form
from fastapi.responses import RedirectResponse

from ..agent import budget, threads, usage


def register(app, deps):
    render, connection, toast = deps.render, deps.connection, deps.toast

    @app.get("/settings")
    def settings_index():
        return RedirectResponse("/settings/assistant", status_code=303)

    def assistant_page(conn, status_code=200, notice=None):
        return render(
            "settings_assistant.html",
            status_code=status_code,
            defaults=threads.defaults(conn),
            efforts=threads.EFFORTS,
            context_limits=threads.CONTEXT_LIMITS,
            toast=notice,
        )

    @app.get("/settings/assistant")
    def assistant_settings():
        with connection() as conn:
            return assistant_page(conn)

    @app.post("/settings/assistant")
    def assistant_save(effort: Annotated[str, Form()] = "", context_limit: Annotated[str, Form()] = ""):
        with connection() as conn:
            try:
                threads.set_defaults(conn, effort, context_limit)
            except threads.OptionError as e:
                return assistant_page(conn, 422, toast("error", "Not saved", str(e)))
            return assistant_page(conn, notice=toast("success", "Saved", "New chat threads start with these defaults."))

    @app.get("/settings/usage")
    def usage_page():
        now = time.time()
        with connection() as conn:
            return render(
                "usage.html",
                budget=budget.status(conn, now),
                summary=usage.summary(conn, now),
                by_kind=usage.by_kind(conn, now),
                by_thread=usage.by_thread(conn, now),
            )
