import time

from fastapi import HTTPException, Request

from ..agent import service, subagents


def register(app, deps):
    render, page, connection = deps.render, deps.page, deps.connection

    def load(conn, run_id):
        run = service.run_view(conn, run_id)
        if run is None:
            raise HTTPException(404, "unknown run")
        return run

    @app.get("/runs/{run_id}")
    def run_page(request: Request, run_id: int):
        with connection() as conn:
            run = load(conn, run_id)
            children = subagents.children_view(conn, run_id, time.time())
            return page(request, "run.html", "_run_children.html", run=run, **children)

    @app.get("/runs/{run_id}/children")
    def run_children(run_id: int):
        with connection() as conn:
            run = load(conn, run_id)
            return render("_run_children.html", run=run, **subagents.children_view(conn, run_id, time.time()))
