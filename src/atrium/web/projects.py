import time
from datetime import datetime
from typing import Annotated

from fastapi import Form, HTTPException, Request
from fastapi.responses import RedirectResponse, Response

from .. import notion_api, notion_wiki
from ..agent import projects, service, threads
from ..agent.tools import notion_write
from ..agent.tools.base import CHAT, NotConfigured, ToolContext, ToolError
from .deps import is_partial

THREAD_LIMIT = 100
ARCHIVED = "archived"


def register(app, deps):
    cfg, render, connection, toast = deps.cfg, deps.render, deps.connection, deps.toast

    def load(conn, project_id):
        found = projects.get(conn, project_id)
        if found is None:
            raise HTTPException(404, "unknown project")
        return found

    def form(project=None, values=None, error=None, status_code=200):
        with connection() as conn:
            chosen = threads.defaults(conn)
        return render("_project_form.html", project=project, values=values or project or {"effort": chosen["effort"], "context_limit": chosen["context_limit"]}, error=error,
                      efforts=threads.EFFORTS, context_limits=threads.CONTEXT_LIMITS, status_code=status_code)

    def submitted(name, instructions, effort, context_limit, wiki_hub_id):
        return {"name": name, "instructions": instructions, "effort": effort, "context_limit": context_limit, "wiki_hub_id": wiki_hub_id}

    def redirect(request, url):
        if is_partial(request):
            return Response(status_code=204, headers={"HX-Redirect": url})
        return RedirectResponse(url, status_code=303)

    @app.get("/projects")
    def projects_index(request: Request, view: str = ""):
        if view not in ("", ARCHIVED):
            raise HTTPException(422, "unknown view")
        with connection() as conn:
            found = projects.list_projects(conn, view == ARCHIVED)
        return render("projects.html", projects=found, view=view)

    @app.get("/projects/new")
    def project_new_form():
        return form()

    @app.post("/projects")
    def project_create(
        request: Request, name: Annotated[str, Form()] = "", instructions: Annotated[str, Form()] = "", effort: Annotated[str, Form()] = "",
        context_limit: Annotated[str, Form()] = "", wiki_hub_id: Annotated[str, Form()] = "",
    ):
        values = submitted(name, instructions, effort, context_limit, wiki_hub_id)
        try:
            with connection() as conn:
                project_id = projects.create(conn, time.time(), **values)
        except projects.ProjectError as e:
            if not is_partial(request):
                raise HTTPException(422, str(e)) from e
            return form(values=values, error=str(e), status_code=422)
        return redirect(request, f"/projects/{project_id}")

    @app.get("/projects/{project_id}")
    def project_page(request: Request, project_id: int, view: str = ""):
        if view not in ("", ARCHIVED):
            raise HTTPException(422, "unknown view")
        with connection() as conn:
            project = load(conn, project_id)
            found = service.list_threads(conn, None, THREAD_LIMIT, view == ARCHIVED, project_id)
        return render("project.html", project=project, threads=found, view=view, q="", project_id=project_id)

    @app.get("/projects/{project_id}/edit")
    def project_edit_form(project_id: int):
        with connection() as conn:
            project = load(conn, project_id)
        return form(project)

    @app.post("/projects/{project_id}")
    def project_update(
        request: Request, project_id: int, name: Annotated[str, Form()] = "", instructions: Annotated[str, Form()] = "", effort: Annotated[str, Form()] = "",
        context_limit: Annotated[str, Form()] = "", wiki_hub_id: Annotated[str, Form()] = "",
    ):
        values = submitted(name, instructions, effort, context_limit, wiki_hub_id)
        with connection() as conn:
            project = load(conn, project_id)
            try:
                projects.update(conn, project_id, time.time(), **values)
            except projects.ProjectError as e:
                if not is_partial(request):
                    raise HTTPException(422, str(e)) from e
                return form(project, values, str(e), 422)
        return redirect(request, f"/projects/{project_id}")

    @app.post("/projects/{project_id}/archive")
    def project_archive(request: Request, project_id: int, archived: Annotated[int, Form()] = 1):
        with connection() as conn:
            if not projects.set_archived(conn, project_id, bool(archived)):
                raise HTTPException(404, "unknown project")
        return redirect(request, "/projects" if archived else f"/projects/{project_id}")

    @app.post("/projects/{project_id}/wiki-hub")
    def project_wiki_hub(request: Request, project_id: int):
        with connection() as conn:
            project = load(conn, project_id)
            if project["wiki_hub_id"]:
                return render("_project_hub.html", project=project, hub_id=project["wiki_hub_id"], notice=None, error=None)
            ctx = ToolContext(conn=conn, cfg=cfg, env=deps.env, kind=CHAT, thread_id=None, run_id=None, allowed_urls=set(), embed=None, now=time.time)
            try:
                page = notion_write.notion_create_page(ctx, {"title": project["name"], "markdown": projects.hub_markdown(project), "sources": [f"atrium project {project_id}"]})
            except (ToolError, NotConfigured, notion_api.NotionError, notion_wiki.WikiError) as e:
                reply = render("_project_hub.html", project=project, hub_id="", notice=None, error=str(e), status_code=422)
                return reply
            projects.set_hub(conn, project_id, page["page_id"], time.time())
            project = load(conn, project_id)
        return render("_project_hub.html", project=project, hub_id=page["page_id"], notice="Wiki hub created under Wiki Home.", error=None)
