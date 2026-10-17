import time
from typing import Annotated

from fastapi import Form, HTTPException

from ..agent import subagents, threads


def register(app, deps):
    render, connection, toast = deps.render, deps.connection, deps.toast

    def show(notice=None, status_code=200, draft=None):
        with connection() as conn:
            return render(
                "subagents.html",
                agents=subagents.list_agents(conn),
                pool=subagents.pool(),
                efforts=threads.EFFORTS,
                draft=draft or {},
                toast=notice,
                status_code=status_code,
            )

    def load(conn, name):
        found = subagents.get(conn, name)
        if found is None:
            raise HTTPException(404, "unknown subagent")
        return found

    @app.get("/settings/subagents")
    def subagents_page():
        return show()

    @app.post("/settings/subagents")
    def subagent_save(
        name: Annotated[str, Form()] = "",
        description: Annotated[str, Form()] = "",
        prompt: Annotated[str, Form()] = "",
        effort: Annotated[str, Form()] = threads.MEDIUM,
        tools: Annotated[list[str], Form()] = [],
    ):
        draft = {"name": name, "description": description, "prompt": prompt, "effort": effort, "tools": tools}
        try:
            with connection() as conn:
                existing = subagents.get(conn, name.strip())
                # The enabled flag has its own toggle; an edit never changes it and a new agent starts enabled.
                subagents.save(conn, name, description, prompt, effort, tools, time.time(), enabled=existing["enabled"] if existing else True)
        except subagents.SubagentError as e:
            return show(toast("error", "Not saved", str(e)), 422, draft)
        return show(toast("success", "Subagent saved", name.strip()))

    @app.post("/settings/subagents/{name}/toggle")
    def subagent_toggle(name: str):
        with connection() as conn:
            found = load(conn, name)
            subagents.set_enabled(conn, name, not found["enabled"], time.time())
        return show(toast("success", "Subagent updated", name))

    @app.post("/settings/subagents/{name}/delete")
    def subagent_delete(name: str):
        try:
            with connection() as conn:
                if not subagents.delete(conn, name):
                    raise HTTPException(404, "unknown subagent")
        except subagents.SubagentError as e:
            return show(toast("error", "Not deleted", str(e)), 422)
        return show(toast("success", "Subagent deleted", name))
