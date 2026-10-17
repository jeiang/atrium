import time

from fastapi.responses import RedirectResponse

from ..agent import budget, usage


def register(app, deps):
    render, connection = deps.render, deps.connection

    @app.get("/settings")
    def settings_index():
        return RedirectResponse("/settings/skills", status_code=303)

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
