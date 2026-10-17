import time

from fastapi import Request

from ..agent import subagents

LIMIT = 50


def register(app, deps):
    render, page, connection = deps.render, deps.page, deps.connection

    @app.get("/agents")
    def agents_page(request: Request):
        with connection() as conn:
            return page(request, "agents.html", "_agent_runs.html", **subagents.recent_runs(conn, time.time(), LIMIT))

    @app.get("/agents/runs")
    def agents_runs():
        with connection() as conn:
            return render("_agent_runs.html", **subagents.recent_runs(conn, time.time(), LIMIT))
